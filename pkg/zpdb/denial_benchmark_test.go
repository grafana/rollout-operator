package zpdb

import (
	"fmt"
	"testing"

	"github.com/go-kit/log"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func newDenialFixture(t testing.TB, replicas int32, partitioned bool) (*EvictionController, *fake.Clientset, admissionv1.AdmissionReview, *corev1.Pod) {
	t.Helper()
	var objects []runtime.Object
	var peer *corev1.Pod
	for _, name := range []string{"worker-left", "worker-right"} {
		sts := newEvictionControllerSts(name)
		sts.Spec.Replicas = &replicas
		sts.Status.Replicas = replicas
		objects = append(objects, sts)
		for i := int32(0); i < replicas; i++ {
			pod := newPod(fmt.Sprintf("%s-%d", name, i), sts)
			if name == "worker-right" && i == 0 {
				pod.Status.Phase = corev1.PodPending
				peer = pod
			}
			objects = append(objects, pod)
		}
	}
	client := fake.NewClientset(objects...)
	c := NewEvictionController(client, newFakeDynamicClient(), testNamespace, newTestPodsFactory(client), log.NewNopLogger(), NewMetrics(prometheus.NewRegistry()))
	budget := newPDBMaxUnavailable(1, rolloutGroupValue)
	if partitioned {
		budget = newPDBMaxUnavailableWithRegex(1, rolloutGroupValue, "worker-.*-([0-9]+)", 1)
	}
	_, _, err := c.cfgObserver.pdbCache.addOrUpdateRaw(budget)
	require.NoError(t, err)
	return c, client, createBasicEvictionAdmissionReview("worker-left-0", testNamespace), peer
}

func BenchmarkEvictionController_HandlePodEvictionRequest_Denied(b *testing.B) {
	for _, mode := range []string{"fresh", "retry"} {
		b.Run(mode, func(b *testing.B) {
			c, client, req, peer := newDenialFixture(b, 256, true)
			cfg, err := c.cfgObserver.pdbCache.find(peer)
			require.NoError(b, err)
			lists := 0
			client.PrependReactor("list", "pods", func(k8stesting.Action) (bool, runtime.Object, error) { lists++; return false, nil, nil })
			// Warm up the retry case. Publishing a new config forces a cold decision in the fresh case.
			require.False(b, c.HandlePodEvictionRequest(b.Context(), req, NewMaxUnavailableZeroOverrideNone()).Allowed)
			lists = 0
			b.ReportAllocs()
			for b.Loop() {
				b.StopTimer()
				client.ClearActions()
				if mode == "fresh" {
					next := *cfg
					next.generation++
					cfg = &next
					c.cfgObserver.pdbCache.addOrUpdate(cfg)
				}
				b.StartTimer()
				if c.HandlePodEvictionRequest(b.Context(), req, NewMaxUnavailableZeroOverrideNone()).Allowed {
					b.Fatal("budget unexpectedly allowed eviction")
				}
			}
			b.ReportMetric(float64(lists)/float64(b.N), "peer-lists/op")
		})
	}
}
