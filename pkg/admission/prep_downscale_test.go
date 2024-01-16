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
	"reflect"
	"testing"
	"text/template"
	"time"

	"github.com/go-kit/log"
	"github.com/go-kit/log/level"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/admission/v1"
	apps "k8s.io/api/apps/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"

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
	testPrepDownscaleWebhook(t, 5, 3, withStatusCode(http.StatusInternalServerError))
	testPrepDownscaleWebhookWithZoneTracker(t, 5, 3, withStatusCode(http.StatusInternalServerError))
}

func TestDownscaleDryRun(t *testing.T) {
	testPrepDownscaleWebhook(t, 5, 3, withDryRun())
	testPrepDownscaleWebhookWithZoneTracker(t, 5, 3, withDryRun())
}

func TestDownscaleWithMinDelayWithoutPreviousPrepareDownscaleCall(t *testing.T) {
	testPrepDownscaleWebhook(t, 5, 3, withMinDelayBeforeShutdown("10m"), withMinDelaySincePrepareDownscaleReached(false))
	testPrepDownscaleWebhookWithZoneTracker(t, 5, 3, withMinDelayBeforeShutdown("10m")) // TODO: min delay is not implemented for zone-tracker.
}

func TestDownscaleWithMinDelayWithPreviousPrepareDownscaleCallButMinTimeNotReachedYet(t *testing.T) {
	ts := time.Now().Add(-5 * time.Minute).UTC()
	testPrepDownscaleWebhook(t, 5, 3, withMinDelayBeforeShutdown("10m"), withLastPreparedPodsTime(ts.Format(time.RFC3339)), withMinDelaySincePrepareDownscaleReached(false))
	testPrepDownscaleWebhookWithZoneTracker(t, 5, 3, withMinDelayBeforeShutdown("10m")) // TODO: min delay is not implemented for zone-tracker.
}

func TestDownscaleWithMinDelayWithPreviousPrepareDownscaleCallAndMinTimeReached(t *testing.T) {
	ts := time.Now().Add(-1 * time.Hour).UTC()
	testPrepDownscaleWebhook(t, 5, 3, withMinDelayBeforeShutdown("10m"), withLastPreparedPodsTime(ts.Format(time.RFC3339)), withMinDelaySincePrepareDownscaleReached(true))
	testPrepDownscaleWebhookWithZoneTracker(t, 5, 3, withMinDelayBeforeShutdown("10m")) // TODO: min delay is not implemented for zone-tracker.
}

func TestInvalidDownscaleMinDelay(t *testing.T) {
	testPrepDownscaleWebhook(t, 5, 3, withMinDelayBeforeShutdown("invalid"))                // Will be ignored.
	testPrepDownscaleWebhookWithZoneTracker(t, 5, 3, withMinDelayBeforeShutdown("invalid")) // TODO: min delay is not implemented for zone-tracker.
}

func TestUpscaleWithLastPreparedPodsTimeWithoutEnablingDeletion(t *testing.T) {
	ts := time.Now().Add(-1 * time.Hour).UTC()

	testPrepDownscaleWebhook(t, 2, 5, withLastPreparedPodsTime(ts.Format(time.RFC3339)), withCheckLastPreparedPodsTimeIsCleared(ts.Format(time.RFC3339)))
	testPrepDownscaleWebhookWithZoneTracker(t, 2, 5) // TODO: feature not implemented for zone-tracker.
}

func TestUpscaleWithLastPreparedPodsTimeWithDeletionEnabled(t *testing.T) {
	ts := time.Now().Add(-1 * time.Hour).UTC()

	testPrepDownscaleWebhook(t, 2, 5, withLastPreparedPodsTime(ts.Format(time.RFC3339)), withDeletePrepareDownscaleEnabled("true"), withCheckLastPreparedPodsTimeIsCleared(""))
	testPrepDownscaleWebhookWithZoneTracker(t, 2, 5) // TODO: feature not implemented for zone-tracker.
}

func TestUpscaleWithLastPreparedPodsTimeWithDeletionDisabled(t *testing.T) {
	ts := time.Now().Add(-1 * time.Hour).UTC()

	testPrepDownscaleWebhook(t, 2, 5, withLastPreparedPodsTime(ts.Format(time.RFC3339)), withDeletePrepareDownscaleEnabled("false"), withCheckLastPreparedPodsTimeIsCleared(ts.Format(time.RFC3339)))
	testPrepDownscaleWebhookWithZoneTracker(t, 2, 5) // TODO: feature not implemented for zone-tracker.
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
	statusCode                    int
	stsAnnotated                  bool
	downscaleInProgress           bool
	allowed                       bool
	dryRun                        bool
	minDelayBeforeShutdown        string
	lastPreparedPodsTime          string // Set on pods before the test.
	deletePrepareDownscaleEnabled string
	checkLastPreparedPodsTime     bool // If enabled, expectedLastPreparedPodsTime is checked on pods after downscale.
	expectedLastPreparedPodsTime  string
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

func withMinDelayBeforeShutdown(val string) optionFunc {
	return func(tp *testParams) {
		tp.minDelayBeforeShutdown = val
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

func withMinDelaySincePrepareDownscaleReached(minDelay bool) optionFunc {
	return func(tp *testParams) {
		if !minDelay {
			tp.allowed = false
		}
	}
}

func withLastPreparedPodsTime(input string) optionFunc {
	return func(tp *testParams) {
		tp.lastPreparedPodsTime = input
	}
}

func withCheckLastPreparedPodsTimeIsCleared(expected string) optionFunc {
	return func(tp *testParams) {
		tp.checkLastPreparedPodsTime = true
		tp.expectedLastPreparedPodsTime = expected
	}
}

func withDeletePrepareDownscaleEnabled(input string) optionFunc {
	return func(tp *testParams) {
		tp.deletePrepareDownscaleEnabled = input
	}
}

type templateParams struct {
	Replicas                     int
	DownScalePathKey             string
	DownScalePath                string
	DownScalePortKey             string
	DownScalePort                string
	DownScaleLabelKey            string
	MinDelayBeforeShutdownKey    string
	MinDelayBeforeShutdownValue  string
	DeleteDownscaleEndpointKey   string
	DeleteDownscaleEndpointValue string
}

type fakeHttpClient struct {
	statusCode int
}

func (f *fakeHttpClient) Do(req *http.Request) (resp *http.Response, err error) {
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

	path := "/prepare-downscale"
	oldParams := templateParams{
		Replicas:          oldReplicas,
		DownScalePathKey:  config.PrepareDownscalePathAnnotationKey,
		DownScalePath:     path,
		DownScalePortKey:  config.PrepareDownscalePortAnnotationKey,
		DownScalePort:     u.Port(),
		DownScaleLabelKey: config.PrepareDownscaleLabelKey,
	}

	newParams := templateParams{
		Replicas:          newReplicas,
		DownScalePathKey:  config.PrepareDownscalePathAnnotationKey,
		DownScalePath:     path,
		DownScalePortKey:  config.PrepareDownscalePortAnnotationKey,
		DownScalePort:     u.Port(),
		DownScaleLabelKey: config.PrepareDownscaleLabelKey,
	}

	if params.minDelayBeforeShutdown != "" {
		oldParams.MinDelayBeforeShutdownKey = config.PrepareDownscaleMinDelayBeforeShutdown
		oldParams.MinDelayBeforeShutdownValue = params.minDelayBeforeShutdown
		newParams.MinDelayBeforeShutdownKey = config.PrepareDownscaleMinDelayBeforeShutdown
		newParams.MinDelayBeforeShutdownValue = params.minDelayBeforeShutdown
	}

	if params.deletePrepareDownscaleEnabled != "" {
		oldParams.DeleteDownscaleEndpointKey = config.PrepareDownscaleDeleteEnabledAnnotationKey
		oldParams.DeleteDownscaleEndpointValue = params.deletePrepareDownscaleEnabled
		newParams.DeleteDownscaleEndpointKey = config.PrepareDownscaleDeleteEnabledAnnotationKey
		newParams.DeleteDownscaleEndpointValue = params.deletePrepareDownscaleEnabled
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
				Labels:    map[string]string{config.RolloutGroupLabelKey: "ingester"},
			},
			// this is needed for pods lookup to work.
			Spec: appsv1.StatefulSetSpec{
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Labels: map[string]string{
							"name": stsName,
						},
					},
				},
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
				Labels:    map[string]string{config.RolloutGroupLabelKey: "ingester"},
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

	// Add individual pods for "main" statefulset.
	for i := 0; i < oldReplicas; i++ {
		podName := fmt.Sprintf("%s-%d", stsName, i)
		pod := &corev1.Pod{
			TypeMeta: metav1.TypeMeta{
				Kind:       "Pod",
				APIVersion: "v1",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name:      podName,
				Namespace: namespace,
				UID:       types.UID(podName),
				Labels: map[string]string{
					"name": stsName,
				},
			},
		}
		if params.lastPreparedPodsTime != "" {
			pod.ObjectMeta.Annotations = map[string]string{
				config.LastPrepareDownscaleAnnotationKey: params.lastPreparedPodsTime,
			}
		}
		objects = append(objects, pod)
	}

	api := fake.NewSimpleClientset(objects...)
	f := &fakeHttpClient{statusCode: params.statusCode}

	admissionResponse := prepareDownscale(ctx, logger, ar, api, f)
	require.Equal(t, params.allowed, admissionResponse.Allowed, "Unexpected result for allowed: got %v, expected %v", admissionResponse.Allowed, params.allowed)

	if params.stsAnnotated {
		// Check that the statefulset now has the last-downscale annotation
		updatedSts, err := api.AppsV1().StatefulSets(namespace).Get(ctx, stsName, metav1.GetOptions{})
		require.NoError(t, err)
		require.NotNil(t, updatedSts.Annotations)
		require.NotNil(t, updatedSts.Annotations[config.LastDownscaleAnnotationKey])
	}

	if params.checkLastPreparedPodsTime {
		sts, err := api.AppsV1().StatefulSets(namespace).Get(ctx, stsName, metav1.GetOptions{})
		require.NoError(t, err)
		pods, err := findPodsForStatefulSet(context.Background(), api, namespace, sts)
		require.NoError(t, err)
		require.Len(t, pods.Items, oldReplicas)

		for _, p := range pods.Items {
			ann, exists := p.Annotations[config.LastPrepareDownscaleAnnotationKey]
			if exists {
				if params.expectedLastPreparedPodsTime == "" {
					require.Failf(t, "annotation test", "unexpected annotation %s exists on pod %s", config.LastPrepareDownscaleAnnotationKey, p.Name)
				} else {
					require.Equal(t, params.expectedLastPreparedPodsTime, ann, "annotation test")
				}
			} else { // annotation doesn't exist
				if params.expectedLastPreparedPodsTime != "" {
					require.Failf(t, "annotation test", "expected annotation %s doesn't exist on pod %s", config.LastPrepareDownscaleAnnotationKey, p.Name)
				}
			}
		}
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
		&apps.StatefulSet{
			ObjectMeta: stsMeta,
			Spec: apps.StatefulSetSpec{
				Template: corev1.PodTemplateSpec{
					ObjectMeta: stsMeta,
				},
			},
			Status: apps.StatefulSetStatus{
				Replicas:        1,
				UpdatedReplicas: 1,
			},
		},
		&apps.StatefulSet{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "zone-b",
				Namespace: namespace,
				Labels:    labels,
			},
			Status: apps.StatefulSetStatus{
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
		&apps.StatefulSet{
			ObjectMeta: stsMeta,
			Spec: apps.StatefulSetSpec{
				Template: corev1.PodTemplateSpec{
					ObjectMeta: stsMeta,
				},
			},
			Status: apps.StatefulSetStatus{
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
	sts := &apps.StatefulSet{
		ObjectMeta: stsMeta,
		Spec: apps.StatefulSetSpec{
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
    rollout-group: "ingester"
  annotations:
    {{.DownScalePathKey}}: {{.DownScalePath}}
    {{.DownScalePortKey}}: "{{.DownScalePort}}"
    {{ if .MinDelayBeforeShutdownKey }}{{.MinDelayBeforeShutdownKey}}: "{{.MinDelayBeforeShutdownValue}}"{{ end }}
    {{ if .DeleteDownscaleEndpointKey }}{{.DeleteDownscaleEndpointKey}}: "{{.DeleteDownscaleEndpointValue}}"{{ end }}
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
	}

	newParams := templateParams{
		Replicas:          newReplicas,
		DownScalePathKey:  config.PrepareDownscalePathAnnotationKey,
		DownScalePath:     path,
		DownScalePortKey:  config.PrepareDownscalePortAnnotationKey,
		DownScalePort:     u.Port(),
		DownScaleLabelKey: config.PrepareDownscaleLabelKey,
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
				Labels:    map[string]string{config.RolloutGroupLabelKey: "ingester"},
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
				Labels:    map[string]string{config.RolloutGroupLabelKey: "ingester"},
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
						config.RolloutGroupLabelKey:                 "ingester",
						config.MinTimeBetweenZonesDownscaleLabelKey: "12h",
					},
				},
			},
		)
	}
	api := fake.NewSimpleClientset(objects...)
	f := &fakeHttpClient{statusCode: params.statusCode}

	zt := newZoneTracker(api, namespace, "zone-tracker-test-cm")

	admissionResponse := zt.prepareDownscale(ctx, logger, ar, api, f)
	require.Equal(t, params.allowed, admissionResponse.Allowed, "Unexpected result for allowed: got %v, expected %v", admissionResponse.Allowed, params.allowed)
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
		name                string
		oldInfo             *objectInfo
		newInfo             *objectInfo
		expected            *v1.AdmissionResponse
		expectedNewReplicas int32
	}{
		{
			name:                "both replicas nil",
			oldInfo:             &objectInfo{},
			newInfo:             &objectInfo{},
			expected:            &v1.AdmissionResponse{Allowed: true},
			expectedNewReplicas: -1,
		},
		{
			name:                "old replicas nil",
			oldInfo:             &objectInfo{},
			newInfo:             &objectInfo{replicas: func() *int32 { i := int32(3); return &i }()},
			expected:            &v1.AdmissionResponse{Allowed: true},
			expectedNewReplicas: 3,
		},
		{
			name:                "new replicas nil",
			oldInfo:             &objectInfo{replicas: func() *int32 { i := int32(3); return &i }()},
			newInfo:             &objectInfo{},
			expected:            &v1.AdmissionResponse{Allowed: true},
			expectedNewReplicas: -1,
		},
		{
			name:                "upscale",
			oldInfo:             &objectInfo{replicas: func() *int32 { i := int32(3); return &i }()},
			newInfo:             &objectInfo{replicas: func() *int32 { i := int32(5); return &i }()},
			expected:            &v1.AdmissionResponse{Allowed: true},
			expectedNewReplicas: 5,
		},
		{
			name:                "no replicas change",
			oldInfo:             &objectInfo{replicas: func() *int32 { i := int32(3); return &i }()},
			newInfo:             &objectInfo{replicas: func() *int32 { i := int32(3); return &i }()},
			expected:            &v1.AdmissionResponse{Allowed: true},
			expectedNewReplicas: 3,
		},
		{
			name:                "downscale",
			oldInfo:             &objectInfo{replicas: func() *int32 { i := int32(5); return &i }()},
			newInfo:             &objectInfo{replicas: func() *int32 { i := int32(3); return &i }()},
			expected:            nil,
			expectedNewReplicas: 3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			newReplicas, actual := checkReplicasChange(logger, tt.oldInfo, tt.newInfo)
			if tt.expectedNewReplicas != newReplicas {
				t.Errorf("newReplicas differ, expected: %d, got: %d", tt.expectedNewReplicas, newReplicas)
			}
			if (actual == nil && tt.expected != nil) || (actual != nil && tt.expected == nil) || (actual != nil && tt.expected != nil && actual.Allowed != tt.expected.Allowed) {
				t.Errorf("checkReplicasChange() = %v, want %v", actual, tt.expected)
			}
		})
	}
}

func TestGetLabelsAndAnnotations(t *testing.T) {
	ctx := context.Background()
	ar := v1.AdmissionReview{}
	api := fake.NewSimpleClientset()

	tests := []struct {
		name        string
		info        *objectInfo
		expectedLbl map[string]string
		expectedAnn map[string]string
		expectErr   bool
	}{
		{
			name: "Deployment",
			info: &objectInfo{
				obj: &appsv1.Deployment{
					ObjectMeta: metav1.ObjectMeta{
						Labels:      map[string]string{"label1": "value1"},
						Annotations: map[string]string{"annotation1": "value1"},
					},
				},
			},
			expectedLbl: map[string]string{"label1": "value1"},
			expectedAnn: map[string]string{"annotation1": "value1"},
			expectErr:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lbls, anns, err := getLabelsAndAnnotations(ctx, ar, api, tt.info)
			if (err != nil) != tt.expectErr {
				t.Errorf("getLabelsAndAnnotations() error = %v, expectErr %v", err, tt.expectErr)
				return
			}
			if !reflect.DeepEqual(lbls, tt.expectedLbl) {
				t.Errorf("getLabelsAndAnnotations() labels = %v, want %v", lbls, tt.expectedLbl)
			}
			if !reflect.DeepEqual(anns, tt.expectedAnn) {
				t.Errorf("getLabelsAndAnnotations() annotations = %v, want %v", anns, tt.expectedAnn)
			}
		})
	}
}

func TestCreatePrepareDownscaleEndpointsFromAdmissionReview(t *testing.T) {
	ar := v1.AdmissionReview{
		Request: &v1.AdmissionRequest{
			Name:      "test",
			Namespace: "default",
		},
	}

	tests := []struct {
		name     string
		oldInfo  *objectInfo
		newInfo  *objectInfo
		port     string
		path     string
		expected []endpoint
	}{
		{
			name: "downscale by 2",
			oldInfo: &objectInfo{
				replicas: func() *int32 { i := int32(5); return &i }(),
			},
			newInfo: &objectInfo{
				replicas: func() *int32 { i := int32(3); return &i }(),
			},
			port: "8080",
			path: "prepare-downscale",
			expected: []endpoint{
				{
					namespace: "default",
					podName:   "test-3",
					url:       "http://test-3.test.default.svc.cluster.local:8080/prepare-downscale",
					index:     3,
				},
				{
					namespace: "default",
					podName:   "test-4",
					url:       "http://test-4.test.default.svc.cluster.local:8080/prepare-downscale",
					index:     4,
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			actual := createPrepareDownscaleEndpointsFromAdmissionReview(ar, tt.oldInfo, tt.newInfo, tt.port, tt.path)
			if len(actual) != len(tt.expected) {
				t.Errorf("createEndpoints() = %v, want %v", actual, tt.expected)
				return
			}
			for i := range actual {
				if tt.expected[i] != actual[i] {
					t.Errorf("createEndpoints() = %v, want %v", actual, tt.expected)
					return
				}
			}
		})
	}
}

func TestCreateEndpoints(t *testing.T) {
	tests := []struct {
		name     string
		from, to int
		port     string
		path     string
		expected []endpoint
	}{
		{
			name: "3 pods",
			from: 2,
			to:   5,
			port: "8080",
			path: "prepare-downscale",
			expected: []endpoint{
				{
					namespace: "default",
					podName:   "test-2",
					url:       "http://test-2.test.default.svc.cluster.local:8080/prepare-downscale",
					index:     2,
				},
				{
					namespace: "default",
					podName:   "test-3",
					url:       "http://test-3.test.default.svc.cluster.local:8080/prepare-downscale",
					index:     3,
				},
				{
					namespace: "default",
					podName:   "test-4",
					url:       "http://test-4.test.default.svc.cluster.local:8080/prepare-downscale",
					index:     4,
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			actual := createEndpoints("default", "test", tt.from, tt.to, tt.port, tt.path)
			if len(actual) != len(tt.expected) {
				t.Errorf("createEndpoints() = %v, want %v", actual, tt.expected)
				return
			}
			for i := range actual {
				if tt.expected[i] != actual[i] {
					t.Errorf("createEndpoints() = %v, want %v", actual, tt.expected)
					return
				}
			}
		})
	}
}
