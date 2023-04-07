package admission

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"testing"
	"text/template"

	"github.com/go-kit/log"
	"github.com/go-kit/log/level"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/admission/v1"
	podv1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
)

func TestUpscale(t *testing.T) {
	testPrepDownscaleWebhook(t, 2, 5)
}

func TestNoReplicasChange(t *testing.T) {
	testPrepDownscaleWebhook(t, 5, 5)
}

func TestValidDownscale(t *testing.T) {
	testPrepDownscaleWebhook(t, 3, 1, withDownscaleAnnotation())
}

func TestInvalidDownscale(t *testing.T) {
	testPrepDownscaleWebhook(t, 5, 3, withStatusCode(http.StatusInternalServerError))
}

func TestDownscaleDryRun(t *testing.T) {
	testPrepDownscaleWebhook(t, 5, 3, withDryRun())
}

func newDebugLogger() log.Logger {
	logger := log.NewLogfmtLogger(log.NewSyncWriter(os.Stderr))
	var options []level.Option
	options = append(options, level.AllowDebug())
	logger = level.NewFilter(logger, options...)
	logger = log.With(logger, "ts", log.DefaultTimestampUTC)
	return logger
}

type testParams struct {
	statusCode   int
	podsPrepared bool
	allowed      bool
	dryRun       bool
}

type optionFunc func(*testParams)

func withStatusCode(statusCode int) optionFunc {
	return func(tp *testParams) {
		tp.statusCode = statusCode
		tp.allowed = true
		if tp.statusCode != http.StatusOK {
			tp.allowed = false
			tp.podsPrepared = false
		}
	}
}

func withDryRun() optionFunc {
	return func(tp *testParams) {
		tp.dryRun = true
		tp.podsPrepared = false
	}
}

func withDownscaleAnnotation() optionFunc {
	return func(tp *testParams) {
		tp.podsPrepared = true
	}
}

type templateParams struct {
	Replicas         int
	DownScalePathKey string
	DownScalePath    string
	DownScalePortKey string
	DownScalePort    string
}

type fakeHttpClient struct {
	statusCode int
}

func (f *fakeHttpClient) Post(url, contentType string, body io.Reader) (resp *http.Response, err error) {
	return &http.Response{
		StatusCode: f.statusCode,
	}, nil
}

func testPrepDownscaleWebhook(t *testing.T, oldReplicas, newReplicas int, options ...optionFunc) {
	params := testParams{
		statusCode:   http.StatusOK,
		podsPrepared: false,
		allowed:      true,
		dryRun:       false,
	}
	for _, option := range options {
		option(&params)
	}

	ctx := context.Background()
	logger := newDebugLogger()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(params.statusCode)
	}))
	defer ts.Close()

	u, err := url.Parse(ts.URL)
	require.NoError(t, err)
	require.NotEmpty(t, u.Port())

	oldParams := templateParams{
		Replicas:         oldReplicas,
		DownScalePathKey: PrepDownscalePathKey,
		DownScalePath:    u.Path,
		DownScalePortKey: PrepDownscalePortKey,
		DownScalePort:    u.Port(),
	}

	newParams := templateParams{
		Replicas:         newReplicas,
		DownScalePathKey: PrepDownscalePathKey,
		DownScalePath:    u.Path,
		DownScalePortKey: PrepDownscalePortKey,
		DownScalePort:    u.Port(),
	}

	rawObject, err := statefulSetTemplate(newParams)
	require.NoError(t, err)

	oldRawObject, err := statefulSetTemplate(oldParams)
	require.NoError(t, err)

	namespace := "test"
	stsName := "my-statefulset"
	ar := v1.AdmissionReview{
		Request: &v1.AdmissionRequest{
			Kind: metav1.GroupVersionKind{
				Group:   "apps",
				Version: "v1",
				Kind:    "Statefulset",
			},
			Resource: metav1.GroupVersionResource{
				Group:    "apps",
				Version:  "v1",
				Resource: "statefulsets",
			},
			Name:      stsName,
			Namespace: namespace,
			Object: runtime.RawExtension{
				Raw: rawObject,
			},
			OldObject: runtime.RawExtension{
				Raw: oldRawObject,
			},
			DryRun: &params.dryRun,
		},
	}
	api := fake.NewSimpleClientset(
		&podv1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "ingester-zone-a-0",
				Namespace: "test",
				Labels: map[string]string{
					"name":                               "my-statefulset",
					"statefulset.kubernetes.io/pod-name": "my-statefulset-0",
				},
			},
		}, &podv1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "ingester-zone-a-1",
				Namespace: "test",
				Labels: map[string]string{
					"name":                               "my-statefulset",
					"statefulset.kubernetes.io/pod-name": "my-statefulset-1",
				},
			},
		}, &podv1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "ingester-zone-a-2",
				Namespace: "test",
				Labels: map[string]string{
					"name":                               "my-statefulset",
					"statefulset.kubernetes.io/pod-name": "my-statefulset-2",
				},
			},
		},
	)
	f := &fakeHttpClient{statusCode: params.statusCode}

	admissionResponse := prepDownscale(ctx, logger, ar, api, f)
	require.Equal(t, params.allowed, admissionResponse.Allowed, "Unexpected result for allowed: got %v, expected %v", admissionResponse.Allowed, params.allowed)

	if params.podsPrepared {
		// Check that one of the pods now has the last-downscale annotation
		client := api.CoreV1().Pods(namespace)
		labelSelector := metav1.LabelSelector{MatchLabels: map[string]string{
			"name":                               stsName,
			"statefulset.kubernetes.io/pod-name": fmt.Sprintf("%v-%v", stsName, 2),
		}}
		pods, err := client.List(ctx,
			metav1.ListOptions{LabelSelector: labels.Set(labelSelector.MatchLabels).String()})
		require.NoError(t, err)

		require.Len(t, pods.Items, 1)
		pod := pods.Items[0]
		require.NotNil(t, pod.Annotations)
		require.NotNil(t, pod.Annotations[LastDownscaleAnnotationKey])
	}
}

func statefulSetTemplate(params templateParams) ([]byte, error) {
	t := template.Must(template.New("sts").Parse(stsTemplate))
	var b bytes.Buffer
	err := t.Execute(&b, params)
	return b.Bytes(), err
}

var stsTemplate = `apiVersion: apps/v1
kind: StatefulSet
metadata:
  name: web
  labels:
    grafana.com/prep-downscale: "true"
    {{.DownScalePathKey}}: {{.DownScalePath}}
    {{.DownScalePortKey}}: "{{.DownScalePort}}"
spec:
  selector:
    matchLabels:
      app: nginx # has to match .spec.template.metadata.labels
  serviceName: "nginx"
  replicas: {{.Replicas }}
  minReadySeconds: 10 # by default is 0
  template:
    metadata:
      labels:
        app: nginx # has to match .spec.selector.matchLabels
    spec:
      terminationGracePeriodSeconds: 10
      containers:
      - name: nginx
        image: registry.k8s.io/nginx-slim:0.8
        ports:
        - containerPort: 80
          name: web
          volumeMounts:
        - name: www
          mountPath: /usr/share/nginx/html
  volumeClaimTemplates:
  - metadata:
    name: www
    spec:
      accessModes: [ "ReadWriteOnce" ]
      storageClassName: "my-storage-class"
      resources:
        requests:
          storage: 1Gi`
