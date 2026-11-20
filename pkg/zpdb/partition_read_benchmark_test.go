package zpdb

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/go-kit/log"
	"github.com/grafana/dskit/spanlogger"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer/protobuf"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	corelisters "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/metadata"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"

	"github.com/grafana/rollout-operator/pkg/util"
)

// Serve pre-encoded API responses so the timed work measures client reads,
// decoding and validation. The transport records the actual response body size.
type podListTransport struct {
	full, partial                       []byte
	fullReads, metaReads, responseBytes int
	metaStatus, fullStatus              int
	lastQuery                           string
}

func (p *podListTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if err := r.Context().Err(); err != nil {
		return nil, err
	}
	p.lastQuery = r.URL.RawQuery
	data, status := p.full, p.fullStatus
	if strings.Contains(r.Header.Get("Accept"), "as=PartialObjectMetadataList") {
		data, status = p.partial, p.metaStatus
		p.metaReads++
	} else {
		p.fullReads++
	}
	header := http.Header{}
	header.Set("Content-Type", runtime.ContentTypeProtobuf)
	if status != 0 {
		header.Set("Content-Type", "application/json")
		data = []byte(fmt.Sprintf(`{"kind":"Status","apiVersion":"v1","status":"Failure","reason":%q,"code":%d}`, strings.ReplaceAll(http.StatusText(status), " ", ""), status))
	} else {
		status = http.StatusOK
	}
	p.responseBytes += len(data)
	return &http.Response{StatusCode: status, Header: header, Body: io.NopCloser(bytes.NewReader(data)), Request: r}, nil
}

func newPodListTransport(t testing.TB, pods []corev1.Pod) *podListTransport {
	t.Helper()
	full := &corev1.PodList{TypeMeta: metav1.TypeMeta{Kind: "PodList", APIVersion: "v1"}, Items: pods}
	partial := &metav1.PartialObjectMetadataList{TypeMeta: metav1.TypeMeta{Kind: "PartialObjectMetadataList", APIVersion: "meta.k8s.io/v1"}}
	for i := range pods {
		partial.Items = append(partial.Items, metav1.PartialObjectMetadata{ObjectMeta: pods[i].ObjectMeta})
	}
	encoder := protobuf.NewSerializer(scheme.Scheme, scheme.Scheme)
	f, err := runtime.Encode(encoder, full)
	require.NoError(t, err)
	m, err := runtime.Encode(encoder, partial)
	require.NoError(t, err)
	return &podListTransport{full: f, partial: m}
}

func enableTestPartitionReads(t testing.TB, c *k8sClient, cfg *rest.Config, httpClient *http.Client, indexer cache.Indexer) {
	t.Helper()
	client, err := metadata.NewForConfigAndClient(cfg, httpClient)
	require.NoError(t, err)
	c.partitionPods = &partitionPodReader{metadata: client.Resource(corev1.SchemeGroupVersion.WithResource("pods")), pods: corelisters.NewPodLister(indexer), fallbacks: prometheus.NewCounter(prometheus.CounterOpts{Name: "fallbacks_total"})}
}

func partitionReadClient(t testing.TB, transport *podListTransport, cached []corev1.Pod) *k8sClient {
	t.Helper()
	cfg := &rest.Config{Host: "https://example.invalid", QPS: -1}
	httpClient := &http.Client{Transport: transport}
	client, err := kubernetes.NewForConfigAndClient(cfg, httpClient)
	require.NoError(t, err)
	indexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{})
	for i := range cached {
		require.NoError(t, indexer.Add(&cached[i]))
	}
	c := &k8sClient{ctx: t.Context(), kubeClient: client}
	enableTestPartitionReads(t, c, cfg, httpClient, indexer)
	return c
}

func partitionReadValidator(ctx context.Context, owner *appsv1.StatefulSet, delay time.Duration) *validatorPartitionAware {
	logger, _ := spanlogger.New(ctx, log.NewNopLogger(), "partition", util.NoTenantResolver{})
	cfg := &config{podNamePartition: regexp.MustCompile(`^worker-.*-([0-9]+)$`), podNamePartitionRegexGroup: 1, crossZoneEvictionDelay: delay}
	return newValidatorPartitionAware(owner, "0", 2, cfg, newPodEvictionCache(), logger)
}

func BenchmarkK8sClient_PodsNotRunningAndReady_Partition(b *testing.B) {
	owner := newEvictionControllerSts("worker-left")
	other := newEvictionControllerSts("worker-right")
	target := newPod("worker-left-0", owner)
	const replicas = 256
	*other.Spec.Replicas = replicas
	other.Status.Replicas = replicas
	pods := make([]corev1.Pod, replicas)
	for i := range pods {
		p := newPod(fmt.Sprintf("worker-right-%d", i), other)
		p.ResourceVersion = fmt.Sprint(i + 1)
		p.Annotations = map[string]string{"example.com/config-revision": "4d59ec12", "example.com/component": "worker"}
		p.ManagedFields = []metav1.ManagedFieldsEntry{{
			Manager: "controller", Operation: metav1.ManagedFieldsOperationUpdate,
			APIVersion: "v1", FieldsType: "FieldsV1",
			FieldsV1: metav1.NewFieldsV1(`{"f:metadata":{"f:labels":{}},"f:spec":{"f:containers":{}}}`),
		}}
		p.Spec.Containers = []corev1.Container{{
			Name: "worker", Image: "example.invalid/worker:v1",
			Args:         []string{"--config=/etc/worker/config.yaml"},
			VolumeMounts: []corev1.VolumeMount{{Name: "config", MountPath: "/etc/worker"}},
			Env: []corev1.EnvVar{{
				Name: "POD_NAME", ValueFrom: &corev1.EnvVarSource{
					FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.name"},
				},
			}},
		}}
		p.Spec.Volumes = []corev1.Volume{{
			Name: "config", VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: "worker-config"}},
			},
		}}
		p.Status.ContainerStatuses = []corev1.ContainerStatus{{
			Name: "worker", Ready: true, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
		}}
		pods[i] = *p
	}
	for _, lagging := range []bool{false, true} {
		b.Run(fmt.Sprintf("lagging=%t", lagging), func(b *testing.B) {
			cachePods := append([]corev1.Pod(nil), pods...)
			if lagging {
				cachePods[0].ResourceVersion = "older"
			}
			transport := newPodListTransport(b, pods)
			c := partitionReadClient(b, transport, cachePods)
			v := partitionReadValidator(b.Context(), owner, 0)
			b.ReportAllocs()
			for b.Loop() {
				result, err := c.podsNotRunningAndReady(other, target, v)
				if err != nil || result.tested != 1 || result.unknown+result.notReady != 0 {
					b.Fatalf("unexpected result %v, %v", result, err)
				}
			}
			b.ReportMetric(float64(transport.responseBytes)/float64(b.N), "response-B/op")
			b.ReportMetric(float64(transport.fullReads+transport.metaReads)/float64(b.N), "requests/op")
		})
	}
}
