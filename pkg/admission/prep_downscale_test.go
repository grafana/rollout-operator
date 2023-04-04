package admission

import (
	"bytes"
	"context"
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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

func TestUpscale(t *testing.T) {
	testPrepDownscaleWebhook(t, 2, 5, http.StatusOK, true, false)
}

func TestNoReplicasChange(t *testing.T) {
	testPrepDownscaleWebhook(t, 5, 5, http.StatusOK, true, false)
}

func TestDownscaleValidDownscale(t *testing.T) {
	testPrepDownscaleWebhook(t, 5, 3, http.StatusOK, true, true)
}

func TestDownscaleInvalidDownscale(t *testing.T) {
	testPrepDownscaleWebhook(t, 5, 3, http.StatusInternalServerError, false, false)
}

func newDebugLogger() log.Logger {
	logger := log.NewLogfmtLogger(log.NewSyncWriter(os.Stderr))
	var options []level.Option
	options = append(options, level.AllowDebug())
	logger = level.NewFilter(logger, options...)
	logger = log.With(logger, "ts", log.DefaultTimestampUTC)
	return logger
}

type templateParams struct {
	Replicas         int
	DownScalePathKey string
	DownScalePath    string
	DownScalePortKey string
	DownScalePort    string
}

func testPrepDownscaleWebhook(t *testing.T, oldReplicas, newReplicas, httpStatusCode int, allowed bool, podsPrepared bool) {
	ctx := context.Background()
	logger := newDebugLogger()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(httpStatusCode)
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
		},
	}
	api := fakeClientSetWithStatefulSet(namespace, stsName)

	admissionResponse := prepDownscale(ctx, logger, ar, api)
	require.Equal(t, allowed, admissionResponse.Allowed, "Unexpected result: got %v, expected %v", admissionResponse.Allowed, allowed)

	if podsPrepared {
		updatedSts, err := api.AppsV1().StatefulSets(namespace).Get(ctx, stsName, metav1.GetOptions{})
		require.NoError(t, err)
		require.NotNil(t, updatedSts.Annotations)
		require.NotNil(t, updatedSts.Annotations[LastDownscaleAnnotationKey])
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
