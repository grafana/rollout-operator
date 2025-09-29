package admission

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"text/template"
	"time"

	"github.com/go-kit/log"
	"github.com/go-kit/log/level"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	admissionv1 "k8s.io/api/admission/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/grafana/rollout-operator/pkg/config"
)

func TestUpscale(t *testing.T) {
	testPrepDownscaleWebhook(t, 2, 5)
	testPrepDownscaleWebhookWithZoneTracker(t, 2, 5)
}

func TestNoReplicasChange(t *testing.T) {
	testPrepDownscaleWebhook(t, 5, 5)
	testPrepDownscaleWebhookWithZoneTracker(t, 5, 5)
}

func TestValidDownscale(t *testing.T) {
	testPrepDownscaleWebhook(t, 3, 1, withDownscaleInProgress(false))
	testPrepDownscaleWebhookWithZoneTracker(t, 3, 1, withDownscaleInProgress(false))
}

func TestValidDownscaleWith204(t *testing.T) {
	testPrepDownscaleWebhook(t, 3, 1, withDownscaleInProgress(false), withStatusCode(http.StatusNoContent))
	testPrepDownscaleWebhookWithZoneTracker(t, 3, 1, withDownscaleInProgress(false), withStatusCode(http.StatusNoContent))
}

func TestValidDownscaleWithAnotherInProgress(t *testing.T) {
	testPrepDownscaleWebhook(t, 3, 1, withDownscaleInProgress(true))
	testPrepDownscaleWebhookWithZoneTracker(t, 3, 1, withDownscaleInProgress(true))
}

func TestInvalidDownscale(t *testing.T) {
	testPrepDownscaleWebhook(t, 5, 3, withStatusCode(http.StatusInternalServerError), expectDeletes(), expectDeny())
	testPrepDownscaleWebhookWithZoneTracker(t, 5, 3, withStatusCode(http.StatusInternalServerError), expectDeletes(), expectDeny())
}

func TestFailLastDownscaleAttr(t *testing.T) {
	testPrepDownscaleWebhook(t, 5, 3, failSetLastDownscale(), expectDeletes(), expectDeny())
	testPrepDownscaleWebhookWithZoneTracker(t, 5, 3, failSetLastDownscale(), expectDeletes(), expectDeny())
}

func TestDownscaleDryRun(t *testing.T) {
	testPrepDownscaleWebhook(t, 5, 3, withDryRun())
	testPrepDownscaleWebhookWithZoneTracker(t, 5, 3, withDryRun())
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
	statusCode           int
	stsAnnotated         bool
	downscaleInProgress  bool
	allowed              bool
	dryRun               bool
	expectDeletes        bool
	failSetLastDownscale bool
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

func expectDeletes() optionFunc {
	return func(tp *testParams) {
		tp.expectDeletes = true
	}
}

func failSetLastDownscale() optionFunc {
	return func(tp *testParams) {
		tp.failSetLastDownscale = true
	}
}

func expectDeny() optionFunc {
	return func(tp *testParams) {
		tp.allowed = false
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
	Replicas          int
	DownScalePathKey  string
	DownScalePath     string
	DownScalePortKey  string
	DownScalePort     string
	DownScaleLabelKey string
	RolloutGroup      string
}

type fakeHttpClient struct {
	mockDo func(*http.Request) (*http.Response, error)

	mu            sync.Mutex
	callsByMethod map[string]int
}

func newFakeHttpClient(mockDo func(*http.Request) (*http.Response, error)) *fakeHttpClient {
	if mockDo == nil {
		panic("mockDo req'd")
	}
	return &fakeHttpClient{
		mockDo:        mockDo,
		callsByMethod: make(map[string]int),
	}
}

func (f *fakeHttpClient) Do(req *http.Request) (resp *http.Response, err error) {
	f.mu.Lock()
	f.callsByMethod[req.Method]++
	f.mu.Unlock()

	return f.mockDo(req)
}

func (f *fakeHttpClient) calls(method string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.callsByMethod[method]
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

	path := "/prepare-downscale"
	oldParams := templateParams{
		Replicas:          oldReplicas,
		DownScalePathKey:  config.PrepareDownscalePathAnnotationKey,
		DownScalePath:     path,
		DownScalePortKey:  config.PrepareDownscalePortAnnotationKey,
		DownScalePort:     u.Port(),
		DownScaleLabelKey: config.PrepareDownscaleLabelKey,
		RolloutGroup:      "ingester",
	}

	newParams := templateParams{
		Replicas:          newReplicas,
		DownScalePathKey:  config.PrepareDownscalePathAnnotationKey,
		DownScalePath:     path,
		DownScalePortKey:  config.PrepareDownscalePortAnnotationKey,
		DownScalePort:     u.Port(),
		DownScaleLabelKey: config.PrepareDownscaleLabelKey,
		RolloutGroup:      "ingester",
	}

	rawObject, err := statefulSetTemplate(newParams)
	require.NoError(t, err)

	oldRawObject, err := statefulSetTemplate(oldParams)
	require.NoError(t, err)

	namespace := "test"
	stsName := "my-statefulset"
	ar := admissionv1.AdmissionReview{
		Request: &admissionv1.AdmissionRequest{
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
		&appsv1.StatefulSet{
			TypeMeta: metav1.TypeMeta{
				Kind:       "StatefulSet",
				APIVersion: "apps/v1",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name:      stsName,
				Namespace: namespace,
				UID:       types.UID(stsName),
				Labels:    map[string]string{config.RolloutGroupLabelKey: "ingester"},
			},
		},
		&appsv1.StatefulSet{
			TypeMeta: metav1.TypeMeta{
				Kind:       "StatefulSet",
				APIVersion: "apps/v1",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name:      stsName + "-1",
				Namespace: namespace,
				UID:       types.UID(stsName),
				Labels:    map[string]string{config.RolloutGroupLabelKey: "ingester"},
			},
		},
	}
	if params.downscaleInProgress {
		objects = append(objects,
			&appsv1.StatefulSet{
				TypeMeta: metav1.TypeMeta{
					Kind:       "StatefulSet",
					APIVersion: "apps/v1",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      stsName + "-2",
					Namespace: namespace,
					UID:       types.UID(stsName),
					Labels: map[string]string{
						config.RolloutGroupLabelKey:                 "ingester",
						config.MinTimeBetweenZonesDownscaleLabelKey: "12h",
					},
					Annotations: map[string]string{
						config.LastDownscaleAnnotationKey: time.Now().UTC().Format(time.RFC3339),
					},
				},
			},
		)
	}
	api := fake.NewSimpleClientset(objects...)

	if params.failSetLastDownscale {
		api.PrependReactor("patch", "statefulsets", func(action k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, errors.New("something terrible happened")
		})
	}

	f := newFakeHttpClient(
		func(r *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: params.statusCode,
				Body:       io.NopCloser(bytes.NewBuffer([]byte(""))),
			}, nil
		})

	admissionResponse := prepareDownscale(ctx, logger, ar, api, f, "cluster.local.")
	require.Equal(t, params.allowed, admissionResponse.Allowed, "Unexpected result for allowed: got %v, expected %v", admissionResponse.Allowed, params.allowed)

	if params.stsAnnotated {
		// Check that the statefulset now has the last-downscale annotation
		updatedSts, err := api.AppsV1().StatefulSets(namespace).Get(ctx, stsName, metav1.GetOptions{})
		require.NoError(t, err)
		require.NotNil(t, updatedSts.Annotations)
		require.NotNil(t, updatedSts.Annotations[config.LastDownscaleAnnotationKey])
	}

	if params.expectDeletes {
		require.Greater(t, f.calls(http.MethodDelete), 0)
	}
}

func TestSendPrepareShutdown(t *testing.T) {
	cases := map[string]struct {
		numEndpoints  int
		lastPostsFail int
		expectErr     bool
	}{
		"no endpoints": {
			numEndpoints:  0,
			lastPostsFail: 0,
			expectErr:     false,
		},
		"all posts succeed": {
			numEndpoints:  64,
			lastPostsFail: 0,
			expectErr:     false,
		},
		"all posts fail": {
			numEndpoints:  64,
			lastPostsFail: 64,
			expectErr:     true,
		},
		"last post fails": {
			numEndpoints:  64,
			lastPostsFail: 1,
			expectErr:     true,
		},
	}

	for n, c := range cases {
		t.Run(n, func(t *testing.T) {
			var postCalls atomic.Int32

			errResponse := &http.Response{
				StatusCode: http.StatusInternalServerError,
				Body:       io.NopCloser(strings.NewReader("we've had a problem")),
			}
			successResponse := &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader("good")),
			}

			httpClient := newFakeHttpClient(
				func(r *http.Request) (*http.Response, error) {
					if r.Method == http.MethodPost {
						calls := postCalls.Add(1)
						if calls > int32(c.numEndpoints-c.lastPostsFail) {
							return errResponse, nil
						} else {
							return successResponse, nil
						}
					}
					panic("unexpected method")
				},
			)

			endpoints := make([]endpoint, 0, c.numEndpoints)
			for i := range cap(endpoints) {
				endpoints = append(endpoints, endpoint{
					url:   fmt.Sprintf("url-something.foo.%d.example.biz", i),
					index: i,
				})
			}

			err := sendPrepareShutdownRequests(context.Background(), log.NewNopLogger(), httpClient, endpoints)
			if c.expectErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
			if c.lastPostsFail > 0 {
				// (It's >= because the goroutines may already have in-flight POSTs before realizing the context is canceled.)
				assert.GreaterOrEqual(t, postCalls.Load(), int32(c.numEndpoints-c.lastPostsFail), "at least |e|-|fails| should have been sent a POST")
			} else {
				assert.Equal(t, int32(c.numEndpoints), postCalls.Load(), "all endpoints should have been sent a POST")
			}
		})
	}
}

func TestUndoPrepareShutdown(t *testing.T) {
	cases := map[string]struct {
		numEndpoints int
		deletesFail  bool
	}{
		"no endpoints": {
			numEndpoints: 0,
			deletesFail:  false,
		},
		"all posts succeed": {
			numEndpoints: 64,
			deletesFail:  false,
		},
		"delete failures should still call all deletes": {
			numEndpoints: 64,
			deletesFail:  true,
		},
	}

	for n, c := range cases {
		t.Run(n, func(t *testing.T) {
			errResponse := &http.Response{
				StatusCode: http.StatusInternalServerError,
				Body:       io.NopCloser(strings.NewReader("we've had a problem")),
			}
			successResponse := &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader("good")),
			}

			httpClient := newFakeHttpClient(
				func(r *http.Request) (*http.Response, error) {
					if r.Method == http.MethodDelete {
						if c.deletesFail {
							return errResponse, nil
						} else {
							return successResponse, nil
						}
					}
					panic("unexpected method")
				},
			)

			endpoints := make([]endpoint, 0, c.numEndpoints)
			for i := range cap(endpoints) {
				endpoints = append(endpoints, endpoint{
					url:   fmt.Sprintf("url-something.foo.%d.example.biz", i),
					index: i,
				})
			}

			undoPrepareShutdownRequests(context.Background(), log.NewNopLogger(), httpClient, endpoints)
			assert.Equal(t, c.numEndpoints, httpClient.calls(http.MethodDelete), "")
		})
	}
}

func TestFindStatefulSetWithNonUpdatedReplicas(t *testing.T) {
	namespace := "test"
	rolloutGroup := "ingester"
	labels := map[string]string{config.RolloutGroupLabelKey: rolloutGroup, "name": "zone-a"}
	stsMeta := metav1.ObjectMeta{
		Name:      "zone-a",
		Namespace: namespace,
		Labels:    labels,
	}
	objects := []runtime.Object{
		&appsv1.StatefulSet{
			ObjectMeta: stsMeta,
			Spec: appsv1.StatefulSetSpec{
				Template: corev1.PodTemplateSpec{
					ObjectMeta: stsMeta,
				},
			},
			Status: appsv1.StatefulSetStatus{
				Replicas:        1,
				UpdatedReplicas: 1,
			},
		},
		&appsv1.StatefulSet{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "zone-b",
				Namespace: namespace,
				Labels:    labels,
			},
			Status: appsv1.StatefulSetStatus{
				Replicas:        1,
				UpdatedReplicas: 1,
			},
		},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "pod1",
				Namespace: namespace,
				Labels:    labels,
			},
			Status: corev1.PodStatus{
				Phase: corev1.PodRunning,
				ContainerStatuses: []corev1.ContainerStatus{{
					Ready: true,
					State: corev1.ContainerState{
						Running: &corev1.ContainerStateRunning{},
					},
				}},
			},
		},
	}
	api := fake.NewSimpleClientset(objects...)

	stsList, err := findStatefulSetsForRolloutGroup(context.Background(), api, namespace, rolloutGroup)
	require.NoError(t, err)

	sts, err := findStatefulSetWithNonUpdatedReplicas(context.Background(), api, namespace, stsList, stsMeta.Name)
	require.NoError(t, err)
	require.NotNil(t, sts)
	assert.Equal(t, sts.name, "zone-b")
}

func TestFindStatefulSetWithNonUpdatedReplicas_UnavailableReplicasSameZone(t *testing.T) {
	namespace := "test"
	rolloutGroup := "ingester"
	labels := map[string]string{config.RolloutGroupLabelKey: rolloutGroup, "name": "zone-a"}
	stsMeta := metav1.ObjectMeta{
		Name:      "zone-a",
		Namespace: namespace,
		Labels:    labels,
	}
	objects := []runtime.Object{
		&appsv1.StatefulSet{
			ObjectMeta: stsMeta,
			Spec: appsv1.StatefulSetSpec{
				Template: corev1.PodTemplateSpec{
					ObjectMeta: stsMeta,
				},
			},
			Status: appsv1.StatefulSetStatus{
				Replicas:        1,
				UpdatedReplicas: 0,
			},
		},
	}
	api := fake.NewSimpleClientset(objects...)

	stsList, err := findStatefulSetsForRolloutGroup(context.Background(), api, namespace, rolloutGroup)
	require.NoError(t, err)

	sts, err := findStatefulSetWithNonUpdatedReplicas(context.Background(), api, namespace, stsList, stsMeta.Name)
	require.NoError(t, err)
	require.Nil(t, sts)
}

func TestFindPodsForStatefulSet(t *testing.T) {
	namespace := "test"
	labels := map[string]string{"name": "sts"}
	stsMeta := metav1.ObjectMeta{
		Name:      "sts",
		Namespace: namespace,
		Labels:    labels,
	}
	sts := &appsv1.StatefulSet{
		ObjectMeta: stsMeta,
		Spec: appsv1.StatefulSetSpec{
			Template: corev1.PodTemplateSpec{
				ObjectMeta: stsMeta,
			},
		},
	}
	objects := []runtime.Object{
		sts,
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "pod1",
				Namespace: namespace,
				Labels:    labels,
			},
		}, &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "pod2",
				Namespace: namespace,
			},
		},
	}

	api := fake.NewSimpleClientset(objects...)
	res, err := findPodsForStatefulSet(context.Background(), api, namespace, sts)
	assert.Nil(t, err)
	require.NotNil(t, res)
	assert.Equal(t, 1, len(res.Items))
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
    {{.DownScaleLabelKey}}: "true"
    rollout-group: "{{.RolloutGroup}}"
  annotations:
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

func testPrepDownscaleWebhookWithZoneTracker(t *testing.T, oldReplicas, newReplicas int, options ...optionFunc) {
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

	path := "/prepare-downscale"
	oldParams := templateParams{
		Replicas:          oldReplicas,
		DownScalePathKey:  config.PrepareDownscalePathAnnotationKey,
		DownScalePath:     path,
		DownScalePortKey:  config.PrepareDownscalePortAnnotationKey,
		DownScalePort:     u.Port(),
		DownScaleLabelKey: config.PrepareDownscaleLabelKey,
		RolloutGroup:      "ingester",
	}

	newParams := templateParams{
		Replicas:          newReplicas,
		DownScalePathKey:  config.PrepareDownscalePathAnnotationKey,
		DownScalePath:     path,
		DownScalePortKey:  config.PrepareDownscalePortAnnotationKey,
		DownScalePort:     u.Port(),
		DownScaleLabelKey: config.PrepareDownscaleLabelKey,
		RolloutGroup:      "ingester",
	}

	rawObject, err := statefulSetTemplate(newParams)
	require.NoError(t, err)

	oldRawObject, err := statefulSetTemplate(oldParams)
	require.NoError(t, err)

	clusterDomain := "cluster.local."
	namespace := "test"
	stsName := "my-statefulset"
	ar := admissionv1.AdmissionReview{
		Request: &admissionv1.AdmissionRequest{
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
		&appsv1.StatefulSet{
			TypeMeta: metav1.TypeMeta{
				Kind:       "StatefulSet",
				APIVersion: "apps/v1",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name:      stsName,
				Namespace: namespace,
				UID:       types.UID(stsName),
				Labels:    map[string]string{config.RolloutGroupLabelKey: "ingester"},
			},
		},
		&appsv1.StatefulSet{
			TypeMeta: metav1.TypeMeta{
				Kind:       "StatefulSet",
				APIVersion: "apps/v1",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name:      stsName + "-1",
				Namespace: namespace,
				UID:       types.UID(stsName),
				Labels:    map[string]string{config.RolloutGroupLabelKey: "ingester"},
			},
		},
	}
	if params.downscaleInProgress {
		objects = append(objects,
			&appsv1.StatefulSet{
				TypeMeta: metav1.TypeMeta{
					Kind:       "StatefulSet",
					APIVersion: "apps/v1",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      stsName + "-2",
					Namespace: namespace,
					UID:       types.UID(stsName),
					Labels: map[string]string{
						config.RolloutGroupLabelKey:                 "ingester",
						config.MinTimeBetweenZonesDownscaleLabelKey: "12h",
					},
				},
			},
		)
	}

	api := fake.NewSimpleClientset(objects...)

	if params.failSetLastDownscale {
		api.PrependReactor("update", "configmaps", func(action k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, errors.New("something terrible happened")
		})
	}

	f := newFakeHttpClient(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: params.statusCode,
			Body:       io.NopCloser(bytes.NewBuffer([]byte(""))),
		}, nil
	})

	zt := newZoneTracker(api, clusterDomain, namespace, "zone-tracker-test-cm")

	admissionResponse := zt.prepareDownscale(ctx, logger, ar, api, f)
	require.Equal(t, params.allowed, admissionResponse.Allowed, "Unexpected result for allowed: got %v, expected %v", admissionResponse.Allowed, params.allowed)

	if params.expectDeletes {
		require.Greater(t, f.calls(http.MethodDelete), 0)
	}
}

func TestDecodeAndReplicas(t *testing.T) {
	raw := []byte(`{"kind":"Deployment","apiVersion":"apps/v1","metadata":{"name":"test-deployment","namespace":"default"},"spec":{"replicas":3}}`)
	info, err := decodeAndReplicas(raw)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if *info.replicas != 3 {
		t.Errorf("expected 3 replicas, got %d", *info.replicas)
	}
	if info.gvk.Kind != "Deployment" {
		t.Errorf("expected kind to be 'Deployment', got '%s'", info.gvk.Kind)
	}
	if info.gvk.GroupVersion().String() != "apps/v1" {
		t.Errorf("expected GroupVersion to be 'apps/v1', got '%s'", info.gvk.GroupVersion().String())
	}
}

func TestCheckReplicasChange(t *testing.T) {
	logger := log.NewNopLogger()

	tests := []struct {
		name     string
		oldInfo  *objectInfo
		newInfo  *objectInfo
		expected *admissionv1.AdmissionResponse
	}{
		{
			name:     "both replicas nil",
			oldInfo:  &objectInfo{},
			newInfo:  &objectInfo{},
			expected: &admissionv1.AdmissionResponse{Allowed: true},
		},
		{
			name:     "old replicas nil",
			oldInfo:  &objectInfo{},
			newInfo:  &objectInfo{replicas: func() *int32 { i := int32(3); return &i }()},
			expected: &admissionv1.AdmissionResponse{Allowed: true},
		},
		{
			name:     "new replicas nil",
			oldInfo:  &objectInfo{replicas: func() *int32 { i := int32(3); return &i }()},
			newInfo:  &objectInfo{},
			expected: &admissionv1.AdmissionResponse{Allowed: true},
		},
		{
			name:     "upscale",
			oldInfo:  &objectInfo{replicas: func() *int32 { i := int32(3); return &i }()},
			newInfo:  &objectInfo{replicas: func() *int32 { i := int32(5); return &i }()},
			expected: &admissionv1.AdmissionResponse{Allowed: true},
		},
		{
			name:     "no replicas change",
			oldInfo:  &objectInfo{replicas: func() *int32 { i := int32(3); return &i }()},
			newInfo:  &objectInfo{replicas: func() *int32 { i := int32(3); return &i }()},
			expected: &admissionv1.AdmissionResponse{Allowed: true},
		},
		{
			name:     "downscale",
			oldInfo:  &objectInfo{replicas: func() *int32 { i := int32(5); return &i }()},
			newInfo:  &objectInfo{replicas: func() *int32 { i := int32(3); return &i }()},
			expected: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			actual := checkReplicasChange(logger, tt.oldInfo, tt.newInfo)
			if (actual == nil && tt.expected != nil) || (actual != nil && tt.expected == nil) || (actual != nil && tt.expected != nil && actual.Allowed != tt.expected.Allowed) {
				t.Errorf("checkReplicasChange() = %v, want %v", actual, tt.expected)
			}
		})
	}
}

func TestGetStatefulSetPrepareInfo(t *testing.T) {
	ctx := context.Background()
	ar := admissionv1.AdmissionReview{}
	api := fake.NewSimpleClientset()

	tests := []struct {
		name       string
		info       *objectInfo
		expectInfo *statefulSetPrepareInfo
		expectErr  bool
	}{
		{
			name: "StatefulSet",
			info: &objectInfo{
				obj: &appsv1.StatefulSet{
					ObjectMeta: metav1.ObjectMeta{
						Labels: map[string]string{
							config.PrepareDownscaleLabelKey: config.PrepareDownscaleLabelValue,
						},
						Annotations: map[string]string{
							config.PrepareDownscalePathAnnotationKey: "path",
							config.PrepareDownscalePortAnnotationKey: "port",
						},
					},
					Spec: appsv1.StatefulSetSpec{
						ServiceName: "serviceName",
					},
				},
			},
			expectInfo: &statefulSetPrepareInfo{
				prepareDownscale: true,
				path:             "path",
				port:             "port",
				serviceName:      "serviceName",
			},
			expectErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stsPrepareInfo, err := getStatefulSetPrepareInfo(ctx, ar, api, tt.info)
			if (err != nil) != tt.expectErr {
				t.Errorf("getStatefulSetPrepareInfo() error = %v, expectErr %v", err, tt.expectErr)
				return
			}
			if !reflect.DeepEqual(stsPrepareInfo, tt.expectInfo) {
				t.Errorf("getStatefulSetPrepareInfo() stsPrepareInfo= %v, want %v", stsPrepareInfo, tt.expectInfo)
			}
		})
	}
}

func TestCreateEndpoints(t *testing.T) {
	ar := admissionv1.AdmissionReview{
		Request: &admissionv1.AdmissionRequest{
			Name:      "test",
			Namespace: "default",
		},
	}

	tests := []struct {
		name          string
		oldInfo       *objectInfo
		newInfo       *objectInfo
		port          string
		path          string
		serviceName   string
		clusterDomain string
		expected      []endpoint
	}{
		{
			name: "downscale by 2",
			oldInfo: &objectInfo{
				replicas: func() *int32 { i := int32(5); return &i }(),
			},
			newInfo: &objectInfo{
				replicas: func() *int32 { i := int32(3); return &i }(),
			},
			port:          "8080",
			path:          "prepare-downscale",
			serviceName:   "service-name",
			clusterDomain: "cluster.local.",
			expected: []endpoint{
				{
					url:   "test-4.service-name.default.svc.cluster.local.:8080/prepare-downscale",
					index: 4,
				},
				{
					url:   "test-3.service-name.default.svc.cluster.local.:8080/prepare-downscale",
					index: 3,
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			actual := createEndpoints(ar, tt.oldInfo, tt.newInfo, tt.port, tt.path, tt.serviceName, tt.clusterDomain)
			if len(actual) != len(tt.expected) {
				t.Errorf("createEndpoints() = %v, want %v", actual, tt.expected)
				return
			}
			for i, ep := range actual {
				if ep.url != tt.expected[i].url || ep.index != tt.expected[i].index {
					t.Errorf("createEndpoints() = %v, want %v", actual, tt.expected)
					return
				}
			}
		})
	}
}
