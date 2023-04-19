package admission

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"testing"
	"text/template"
	"time"

	"github.com/go-kit/log"
	"github.com/go-kit/log/level"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/admission/v1"
	apps "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
)

func TestUpscale(t *testing.T) {
	testPrepDownscaleWebhook(t, 2, 5)
}

func TestNoReplicasChange(t *testing.T) {
	testPrepDownscaleWebhook(t, 5, 5)
}

func TestValidDownscale(t *testing.T) {
	testPrepDownscaleWebhook(t, 3, 1, withDownscaleInProgress(false))
}

func TestValidDownscaleWith204(t *testing.T) {
	testPrepDownscaleWebhook(t, 3, 1, withDownscaleInProgress(false), withStatusCode(http.StatusNoContent))
}

func TestValidDownscaleWithAnotherInProgress(t *testing.T) {
	testPrepDownscaleWebhook(t, 3, 1, withDownscaleInProgress(true))
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
	statusCode          int
	stsAnnotated        bool
	downscaleInProgress bool
	allowed             bool
	dryRun              bool
}

type optionFunc func(*testParams)

func withStatusCode(statusCode int) optionFunc {
	return func(tp *testParams) {
		tp.statusCode = statusCode
		if tp.statusCode/100 != 2 {
			tp.allowed = false
			tp.stsAnnotated = false
		}
	}
}

func withDryRun() optionFunc {
	return func(tp *testParams) {
		tp.dryRun = true
		tp.stsAnnotated = false
	}
}

func withDownscaleInProgress(inProgress bool) optionFunc {
	return func(tp *testParams) {
		tp.stsAnnotated = true
		tp.allowed = true
		if inProgress {
			tp.stsAnnotated = false
			tp.allowed = false
		}
		tp.downscaleInProgress = inProgress
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
		Body:       io.NopCloser(bytes.NewBuffer([]byte(""))),
	}, nil
}

func testPrepDownscaleWebhook(t *testing.T, oldReplicas, newReplicas int, options ...optionFunc) {
	params := testParams{
		statusCode:          http.StatusOK,
		stsAnnotated:        false,
		downscaleInProgress: false,
		allowed:             true,
		dryRun:              false,
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

	path := "/prep-downscale"
	oldParams := templateParams{
		Replicas:         oldReplicas,
		DownScalePathKey: PrepDownscalePathKey,
		DownScalePath:    path,
		DownScalePortKey: PrepDownscalePortKey,
		DownScalePort:    u.Port(),
	}

	newParams := templateParams{
		Replicas:         newReplicas,
		DownScalePathKey: PrepDownscalePathKey,
		DownScalePath:    path,
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
	objects := []runtime.Object{
		&apps.StatefulSet{
			TypeMeta: metav1.TypeMeta{
				Kind:       "StatefulSet",
				APIVersion: "apps/v1",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name:      stsName,
				Namespace: namespace,
				UID:       types.UID(stsName),
				Labels:    map[string]string{RolloutGroupLabelKey: "ingester"},
			},
		},
		&apps.StatefulSet{
			TypeMeta: metav1.TypeMeta{
				Kind:       "StatefulSet",
				APIVersion: "apps/v1",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name:      stsName + "-1",
				Namespace: namespace,
				UID:       types.UID(stsName),
				Labels:    map[string]string{RolloutGroupLabelKey: "ingester"},
			},
		},
	}
	if params.downscaleInProgress {
		objects = append(objects,
			&apps.StatefulSet{
				TypeMeta: metav1.TypeMeta{
					Kind:       "StatefulSet",
					APIVersion: "apps/v1",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      stsName + "-2",
					Namespace: namespace,
					UID:       types.UID(stsName),
					Labels: map[string]string{
						RolloutGroupLabelKey:                   "ingester",
						LastDownscaleAnnotationKey:             time.Now().UTC().Format(time.RFC3339),
						TimeBetweenZonesDownscaleAnnotationKey: "12h",
					},
				},
			},
		)
	}
	api := fake.NewSimpleClientset(objects...)
	f := &fakeHttpClient{statusCode: params.statusCode}

	admissionResponse := prepDownscale(ctx, logger, ar, api, f)
	require.Equal(t, params.allowed, admissionResponse.Allowed, "Unexpected result for allowed: got %v, expected %v", admissionResponse.Allowed, params.allowed)

	if params.stsAnnotated {
		// Check that the statefulset now has the last-downscale annotation
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
    rollout-group: "ingester"
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
