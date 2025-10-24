package zpdb

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/go-logfmt/logfmt"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	admissionv1 "k8s.io/api/admission/v1"
	appsv1 "k8s.io/api/apps/v1"
	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"

	rolloutconfig "github.com/grafana/rollout-operator/pkg/config"
)

const (
	statefulSetZoneA  = "ingester-zone-a"
	statefulSetZoneB  = "ingester-zone-b"
	statefulSetZoneC  = "ingester-zone-c"
	testPodZoneA0     = "ingester-zone-a-0"
	testPodZoneA1     = "ingester-zone-a-1"
	testPodZoneA2     = "ingester-zone-a-2"
	testPodZoneB0     = "ingester-zone-b-0"
	testPodZoneB1     = "ingester-zone-b-1"
	testPodZoneB2     = "ingester-zone-b-2"
	testPodZoneC0     = "ingester-zone-c-0"
	testPodZoneC1     = "ingester-zone-c-1"
	testPodZoneC2     = "ingester-zone-c-2"
	rolloutGroupValue = "ingester"
	nameLabel         = "name"
)

type testContext struct {
	ctx        context.Context
	logs       *dummyLogger
	request    admissionv1.AdmissionReview
	controller *EvictionController
}

// A dummyLogger accumulates log lines into a slice so we can assert that certain logs were recorded
type dummyLogger struct {
	logs []string
}

func newDummyLogger() *dummyLogger {
	return &dummyLogger{
		logs: make([]string, 0),
	}
}

func (d *dummyLogger) Log(keyVals ...interface{}) error {
	var buf bytes.Buffer
	enc := logfmt.NewEncoder(&buf)

	for i := 0; i < len(keyVals); i += 2 {
		_ = enc.EncodeKeyval(keyVals[i], keyVals[i+1])
	}
	_ = enc.EndRecord()

	d.logs = append(d.logs, buf.String())
	return nil
}

// assertHasLog ensures there is a log line which contains all the given elements
// Note that we do not care about the ordering of the elements
func (d *dummyLogger) assertHasLog(t *testing.T, elements []string) {

	for _, line := range d.logs {
		found := true
		for _, element := range elements {
			if !strings.Contains(line, element) {
				found = false
				break
			}
		}
		if found {
			return
		}
	}

	require.Fail(t, "Expected log not found", strings.Join(elements, ", "))
}

func newTestContext(t *testing.T, request admissionv1.AdmissionReview, pdbRawConfig *unstructured.Unstructured, objects ...runtime.Object) *testContext {
	testCtx := &testContext{
		ctx:     context.Background(),
		logs:    newDummyLogger(),
		request: request,
	}

	testCtx.controller = NewEvictionController(fake.NewClientset(objects...), newFakeDynamicClient(), testNamespace, testCtx.logs)
	require.NoError(t, testCtx.controller.Start())

	if pdbRawConfig != nil {
		_, _, _ = testCtx.controller.cfgObserver.pdbCache.addOrUpdateRaw(pdbRawConfig)
	}
	return testCtx
}

func newTestContextWithoutAdmissionReview(t *testing.T, pdbRawConfig *unstructured.Unstructured, objects ...runtime.Object) *testContext {
	testCtx := &testContext{
		ctx:  context.Background(),
		logs: newDummyLogger(),
	}

	testCtx.controller = NewEvictionController(fake.NewClientset(objects...), newFakeDynamicClient(), testNamespace, testCtx.logs)
	require.NoError(t, testCtx.controller.Start())

	if pdbRawConfig != nil {
		_, _, _ = testCtx.controller.cfgObserver.pdbCache.addOrUpdateRaw(pdbRawConfig)
	}
	return testCtx
}

func (c *testContext) assertDenyResponse(t *testing.T, reason string, statusCode int) {
	response := c.controller.HandlePodEvictionRequest(c.ctx, c.request)
	require.NotNil(t, response.UID)
	require.False(t, response.Allowed)
	require.Equal(t, reason, response.Result.Message)
	require.Equal(t, int32(statusCode), response.Result.Code)
}

func (c *testContext) assertDenyResponseViaMarkPodAsDeleted(t *testing.T, pod string, reason string) {
	response := c.controller.MarkPodAsDeleted(c.ctx, testNamespace, pod, "eviction-controller-test")
	require.ErrorContains(t, response, reason)
}

func (c *testContext) assertAllowResponseViaMarkPodAsDeleted(t *testing.T, pod string) {
	response := c.controller.MarkPodAsDeleted(t.Context(), testNamespace, pod, "eviction-controller-test")
	require.NoError(t, response)
}

func (c *testContext) assertAllowResponse(t *testing.T) {
	response := c.controller.HandlePodEvictionRequest(c.ctx, c.request)
	require.NotNil(t, response.UID)
	require.True(t, response.Allowed)
}

func (c *testContext) assertAllowResponseWithWarning(t *testing.T, warning string) {
	response := c.controller.HandlePodEvictionRequest(c.ctx, c.request)
	require.NotNil(t, response.UID)
	require.True(t, response.Allowed)
	require.Equal(t, warning, response.Warnings[0])
}

func TestPodEviction_NotCreateEvent(t *testing.T) {
	ar := createBasicEvictionAdmissionReview(testPodZoneA0, testNamespace)
	ar.Request.Operation = admissionv1.Delete
	testCtx := newTestContext(t, ar, nil)
	defer testCtx.controller.Stop()
	testCtx.assertDenyResponse(t, "request operation is not create, got: DELETE", 400)

	expectedLogEntry := []string{
		"method=admission.PodEviction()",
		"object.name=" + testPodZoneA0,
		"object.namespace=" + testNamespace,
		"object.resource=evictions",
		"request.uid=test-request-uid",
		`msg="pod eviction denied"`,
		`reason="not a valid create pod eviction request"`,
	}
	testCtx.logs.assertHasLog(t, expectedLogEntry)

}

func TestPodEviction_NotEvictionSubResource(t *testing.T) {
	ar := createBasicEvictionAdmissionReview(testPodZoneA0, testNamespace)
	ar.Request.SubResource = "foo"
	testCtx := newTestContext(t, ar, nil)
	defer testCtx.controller.Stop()
	testCtx.assertDenyResponse(t, "request SubResource is not eviction, got: foo", 400)
}

func TestPodEviction_EmptyName(t *testing.T) {
	testCtx := newTestContext(t, createBasicEvictionAdmissionReview("", testNamespace), nil)
	defer testCtx.controller.Stop()
	testCtx.assertDenyResponse(t, "request did not include both a namespace and a name", 400)
}

func TestPodEviction_PodNotFound(t *testing.T) {
	testCtx := newTestContext(t, createBasicEvictionAdmissionReview(testPodZoneA0, testNamespace), nil)
	defer testCtx.controller.Stop()
	testCtx.assertDenyResponse(t, `pods "ingester-zone-a-0" not found`, 400)
	testCtx.logs.assertHasLog(t, []string{`reason="unable to find pod by name"`})
}

func TestPodEviction_PodNotReady(t *testing.T) {
	pod := newPodNoOwner(testPodZoneA0)
	pod.Status.Phase = corev1.PodFailed
	testCtx := newTestContext(t, createBasicEvictionAdmissionReview(testPodZoneA0, testNamespace), nil, pod)
	defer testCtx.controller.Stop()
	testCtx.assertAllowResponseWithWarning(t, "pod is not ready")
	testCtx.logs.assertHasLog(t, []string{`reason="pod is not ready"`})
}

func TestPodEviction_PodWithNoOwner(t *testing.T) {
	pod := newPodNoOwner(testPodZoneA0)
	testCtx := newTestContext(t, createBasicEvictionAdmissionReview(testPodZoneA0, testNamespace), newPDBMaxUnavailable(1, rolloutGroupValue), pod)
	defer testCtx.controller.Stop()
	testCtx.assertDenyResponse(t, "unable to find a StatefulSet pod owner", 500)
	testCtx.logs.assertHasLog(t, []string{`reason="unable to find pod owner"`})
}

func TestPodEviction_UnableToRetrievePdbConfig(t *testing.T) {
	sts := newEvictionControllerSts(statefulSetZoneA)
	pod := newPod(testPodZoneA0, sts)
	testCtx := newTestContext(t, createBasicEvictionAdmissionReview(testPodZoneA0, testNamespace), nil, pod, sts)
	defer testCtx.controller.Stop()
	testCtx.assertAllowResponse(t)
}

func TestPodEviction_MaxUnavailableEq0(t *testing.T) {
	sts := newEvictionControllerSts(statefulSetZoneA)
	pod := newPod(testPodZoneA0, sts)
	testCtx := newTestContext(t, createBasicEvictionAdmissionReview(testPodZoneA0, testNamespace), newPDBMaxUnavailable(0, rolloutGroupValue), pod, sts)
	defer testCtx.controller.Stop()
	testCtx.assertDenyResponse(t, "max unavailable = 0", 403)
	testCtx.logs.assertHasLog(t, []string{`reason="max unavailable = 0"`})
}

func TestPodEviction_MaxUnavailableEq0_ViaAllowPodEviction(t *testing.T) {
	sts := newEvictionControllerSts(statefulSetZoneA)
	pod := newPod(testPodZoneA0, sts)
	testCtx := newTestContextWithoutAdmissionReview(t, newPDBMaxUnavailable(0, rolloutGroupValue), pod, sts)
	defer testCtx.controller.Stop()
	testCtx.assertDenyResponseViaMarkPodAsDeleted(t, testPodZoneA0, "max unavailable = 0")
	testCtx.logs.assertHasLog(t, []string{`reason="max unavailable = 0"`})
}

func TestPodEviction_Allowed_ViaAllowPodEviction(t *testing.T) {
	objs := make([]runtime.Object, 0, 12)
	objs = append(objs, newEvictionControllerSts(statefulSetZoneA))
	objs = append(objs, newEvictionControllerSts(statefulSetZoneB))
	objs = append(objs, newEvictionControllerSts(statefulSetZoneC))

	for _, p := range []string{testPodZoneA0, testPodZoneA1, testPodZoneA2} {
		objs = append(objs, newPod(p, objs[0].(*appsv1.StatefulSet)))
	}
	for _, p := range []string{testPodZoneB0, testPodZoneB1, testPodZoneB2} {
		objs = append(objs, newPod(p, objs[1].(*appsv1.StatefulSet)))
	}
	for _, p := range []string{testPodZoneC0, testPodZoneC1, testPodZoneC2} {
		objs = append(objs, newPod(p, objs[2].(*appsv1.StatefulSet)))
	}

	zoneAPod0 := objs[3].(*corev1.Pod)
	zoneAPod2 := objs[5].(*corev1.Pod)

	testCtx := newTestContextWithoutAdmissionReview(t, newPDBMaxUnavailable(1, rolloutGroupValue), objs...)
	defer testCtx.controller.Stop()
	require.False(t, testCtx.controller.podObserver.podEvictCache.hasPendingEviction(zoneAPod0))
	// note that we do not stop the controller after this test
	testCtx.assertAllowResponseViaMarkPodAsDeleted(t, zoneAPod0.Name)
	require.True(t, testCtx.controller.podObserver.podEvictCache.hasPendingEviction(zoneAPod0))
	testCtx.assertDenyResponseViaMarkPodAsDeleted(t, zoneAPod2.Name, "1 pod not ready in ingester-zone-a")
}

func TestPodEviction_MaxUnavailablePercentageEq0(t *testing.T) {
	sts := newEvictionControllerSts(statefulSetZoneA)
	pod := newPod(testPodZoneA0, sts)
	testCtx := newTestContext(t, createBasicEvictionAdmissionReview(testPodZoneA0, testNamespace), newPDBMaxUnavailablePercent(0, rolloutGroupValue), pod, sts)
	defer testCtx.controller.Stop()
	testCtx.assertDenyResponse(t, "max unavailable = 0", 403)
	testCtx.logs.assertHasLog(t, []string{`reason="max unavailable = 0"`})
}

// TestPodEviction_SingleZoneMultiplePodsUpscale - only 1 StatefulSet has been found
func TestPodEviction_SingleZoneMultiplePodsUpscale(t *testing.T) {
	sts := newEvictionControllerSts(statefulSetZoneA)
	pod0 := newPod(testPodZoneA0, sts)
	pod1 := newPod(testPodZoneA1, sts)

	testCtx := newTestContext(t, createBasicEvictionAdmissionReview(testPodZoneA0, testNamespace), newPDBMaxUnavailable(1, rolloutGroupValue), pod0, pod1, sts)
	defer testCtx.controller.Stop()
	testCtx.assertDenyResponse(t, "minimum number of StatefulSets not found", 400)
}

// TestPodEviction_MultiZoneClassic tests a classic multi-zone topology.
// There are 3 StatefulSets (zone a, b, c) and each has 3 pods.
func TestPodEviction_MultiZoneClassic(t *testing.T) {

	objs := make([]runtime.Object, 0, 12)
	objs = append(objs, newEvictionControllerSts(statefulSetZoneA))
	objs = append(objs, newEvictionControllerSts(statefulSetZoneB))
	objs = append(objs, newEvictionControllerSts(statefulSetZoneC))

	for _, p := range []string{testPodZoneA0, testPodZoneA1, testPodZoneA2} {
		objs = append(objs, newPod(p, objs[0].(*appsv1.StatefulSet)))
	}
	for _, p := range []string{testPodZoneB0, testPodZoneB1, testPodZoneB2} {
		objs = append(objs, newPod(p, objs[1].(*appsv1.StatefulSet)))
	}
	for _, p := range []string{testPodZoneC0, testPodZoneC1, testPodZoneC2} {
		objs = append(objs, newPod(p, objs[2].(*appsv1.StatefulSet)))
	}

	stsZoneB := objs[1].(*appsv1.StatefulSet)
	zoneAPod0 := objs[3].(*corev1.Pod)
	zoneAPod2 := objs[5].(*corev1.Pod)
	zoneCPod2 := objs[11].(*corev1.Pod)

	// allow eviction since all other pods are healthy
	// also check that the evicted pod has been stored in the eviction configCache
	testCtx := newTestContext(t, createBasicEvictionAdmissionReview(testPodZoneA0, testNamespace), newPDBMaxUnavailable(1, rolloutGroupValue), objs...)
	require.False(t, testCtx.controller.podObserver.podEvictCache.hasPendingEviction(zoneAPod0))
	testCtx.assertAllowResponse(t)
	require.True(t, testCtx.controller.podObserver.podEvictCache.hasPendingEviction(zoneAPod0))
	testCtx.controller.Stop()

	// mark a pod in the same zone as failed - with maxUnavailable=1 this will be denied
	zoneAPod2.Status.Phase = corev1.PodFailed
	testCtx = newTestContext(t, createBasicEvictionAdmissionReview(testPodZoneA0, testNamespace), newPDBMaxUnavailable(1, rolloutGroupValue), objs...)
	testCtx.assertDenyResponse(t, "1 pod not ready in ingester-zone-a", 429)
	testCtx.controller.Stop()

	// mark a pod in the same zone as failed - with maxUnavailable=2 this will be allowed
	testCtx = newTestContext(t, createBasicEvictionAdmissionReview(testPodZoneA0, testNamespace), newPDBMaxUnavailable(2, rolloutGroupValue), objs...)
	testCtx.assertAllowResponse(t)
	testCtx.controller.Stop()

	// mark a pod in the another zone as failed - we will deny this eviction
	zoneCPod2.Status.Phase = corev1.PodFailed
	testCtx = newTestContext(t, createBasicEvictionAdmissionReview(testPodZoneA0, testNamespace), newPDBMaxUnavailable(1, rolloutGroupValue), objs...)
	testCtx.assertDenyResponse(t, "1 pod not ready in ingester-zone-c", 429)
	testCtx.controller.Stop()

	// mark a pod in the another zone as failed - we will deny this eviction even if max unavailable = 2
	testCtx = newTestContext(t, createBasicEvictionAdmissionReview(testPodZoneA0, testNamespace), newPDBMaxUnavailable(2, rolloutGroupValue), objs...)
	testCtx.assertDenyResponse(t, "1 pod not ready in ingester-zone-c", 429)
	testCtx.controller.Stop()

	// reset so all the pods are reporting running
	zoneAPod2.Status.Phase = corev1.PodRunning
	zoneCPod2.Status.Phase = corev1.PodRunning

	// but zone b sts has more replicas than we will see pods for
	stsZoneB.Status.Replicas = 4
	testCtx = newTestContext(t, createBasicEvictionAdmissionReview(testPodZoneA0, testNamespace), newPDBMaxUnavailable(1, rolloutGroupValue), objs...)
	testCtx.assertDenyResponse(t, "1 pod unknown in ingester-zone-b", 429)
	testCtx.controller.Stop()
}

func TestPodEviction_PartitionZones(t *testing.T) {

	objs := make([]runtime.Object, 0, 12)
	objs = append(objs, newEvictionControllerSts(statefulSetZoneA))
	objs = append(objs, newEvictionControllerSts(statefulSetZoneB))
	objs = append(objs, newEvictionControllerSts(statefulSetZoneC))

	for _, p := range []string{testPodZoneA0, testPodZoneA1, testPodZoneA2} {
		objs = append(objs, newPod(p, objs[0].(*appsv1.StatefulSet)))
	}
	for _, p := range []string{testPodZoneB0, testPodZoneB1, testPodZoneB2} {
		objs = append(objs, newPod(p, objs[1].(*appsv1.StatefulSet)))
	}
	for _, p := range []string{testPodZoneC0, testPodZoneC1, testPodZoneC2} {
		objs = append(objs, newPod(p, objs[2].(*appsv1.StatefulSet)))
	}

	stsZoneB := objs[1].(*appsv1.StatefulSet)
	zoneAPod1 := objs[4].(*corev1.Pod)
	zoneBPod0 := objs[6].(*corev1.Pod)
	zoneCPod2 := objs[11].(*corev1.Pod)

	// ingester-zone-a-0 --> 0
	podPartitionZoneRegex := "[a-z\\-]+-zone-[a-z]-([0-9]+)"

	testCtx := newTestContext(t, createBasicEvictionAdmissionReview(testPodZoneA0, testNamespace), newPDBMaxUnavailableWithRegex(1, rolloutGroupValue, podPartitionZoneRegex, int64(1)), objs...)
	testCtx.assertAllowResponse(t)
	testCtx.controller.Stop()

	// mark a pod in the same zone as failed - we will allow this eviction as it's in a different partition
	zoneAPod1.Status.Phase = corev1.PodFailed
	testCtx = newTestContext(t, createBasicEvictionAdmissionReview(testPodZoneA0, testNamespace), newPDBMaxUnavailableWithRegex(1, rolloutGroupValue, podPartitionZoneRegex, int64(1)), objs...)
	testCtx.assertAllowResponse(t)
	testCtx.controller.Stop()

	// mark a pod in the another zone + partition as failed - we will allow this eviction as it's in a different partition
	zoneCPod2.Status.Phase = corev1.PodFailed
	testCtx = newTestContext(t, createBasicEvictionAdmissionReview(testPodZoneA0, testNamespace), newPDBMaxUnavailableWithRegex(1, rolloutGroupValue, podPartitionZoneRegex, int64(1)), objs...)
	testCtx.assertAllowResponse(t)
	testCtx.controller.Stop()

	// mark a pod in the another zone + same partition as failed - we will deny this eviction
	zoneBPod0.Status.Phase = corev1.PodFailed
	testCtx = newTestContext(t, createBasicEvictionAdmissionReview(testPodZoneA0, testNamespace), newPDBMaxUnavailableWithRegex(1, rolloutGroupValue, podPartitionZoneRegex, int64(1)), objs...)
	testCtx.assertDenyResponse(t, "1 pod not ready in partition 0", 429)
	testCtx.controller.Stop()

	// reset so all the pods are reporting running
	zoneAPod1.Status.Phase = corev1.PodRunning
	zoneBPod0.Status.Phase = corev1.PodRunning
	zoneCPod2.Status.Phase = corev1.PodRunning

	// but zone b sts has more replicas than we will see pods for
	stsZoneB.Status.Replicas = 4
	testCtx = newTestContext(t, createBasicEvictionAdmissionReview(testPodZoneA0, testNamespace), newPDBMaxUnavailableWithRegex(1, rolloutGroupValue, podPartitionZoneRegex, int64(1)), objs...)
	testCtx.assertDenyResponse(t, "1 pod unknown in partition 0", 429)
	testCtx.controller.Stop()

	testCtx = newTestContext(t, createBasicEvictionAdmissionReview(testPodZoneA0, testNamespace), newPDBMaxUnavailableWithRegex(1, rolloutGroupValue, "ingester(-foo)?-zone-[a-z]-([0-9]+)", int64(2)), objs...)
	testCtx.assertDenyResponse(t, "1 pod unknown in partition 0", 429)
	testCtx.controller.Stop()
}

func TestPodEviction_PartitionZonesMaxUnavailable2(t *testing.T) {

	objs := make([]runtime.Object, 0, 12)
	objs = append(objs, newEvictionControllerSts(statefulSetZoneA))
	objs = append(objs, newEvictionControllerSts(statefulSetZoneB))
	objs = append(objs, newEvictionControllerSts(statefulSetZoneC))

	for _, p := range []string{testPodZoneA0, testPodZoneA1, testPodZoneA2} {
		objs = append(objs, newPod(p, objs[0].(*appsv1.StatefulSet)))
	}
	for _, p := range []string{testPodZoneB0, testPodZoneB1, testPodZoneB2} {
		objs = append(objs, newPod(p, objs[1].(*appsv1.StatefulSet)))
	}
	for _, p := range []string{testPodZoneC0, testPodZoneC1, testPodZoneC2} {
		objs = append(objs, newPod(p, objs[2].(*appsv1.StatefulSet)))
	}

	zoneBPod0 := objs[6].(*corev1.Pod)
	zoneCPod0 := objs[9].(*corev1.Pod)

	// ingester-zone-a-0 --> 0
	podPartitionZoneRegex := "[a-z\\-]+-zone-[a-z]-([0-9]+)"

	// mark 1 pod in the another zone + same partition as failed
	zoneBPod0.Status.Phase = corev1.PodFailed
	testCtx := newTestContext(t, createBasicEvictionAdmissionReview(testPodZoneA0, testNamespace), newPDBMaxUnavailableWithRegex(1, rolloutGroupValue, podPartitionZoneRegex, int64(1)), objs...)
	testCtx.assertDenyResponse(t, "1 pod not ready in partition 0", 429)
	testCtx.controller.Stop()

	testCtx = newTestContext(t, createBasicEvictionAdmissionReview(testPodZoneA0, testNamespace), newPDBMaxUnavailableWithRegex(2, rolloutGroupValue, podPartitionZoneRegex, int64(1)), objs...)
	testCtx.assertAllowResponse(t)
	testCtx.controller.Stop()

	// mark another pod in the another zone + same partition as failed
	zoneCPod0.Status.Phase = corev1.PodFailed
	testCtx = newTestContext(t, createBasicEvictionAdmissionReview(testPodZoneA0, testNamespace), newPDBMaxUnavailableWithRegex(2, rolloutGroupValue, podPartitionZoneRegex, int64(1)), objs...)
	testCtx.assertDenyResponse(t, "2 pods not ready in partition 0", 429)
	testCtx.controller.Stop()
}

// createBasicEvictionAdmissionReview returns a pod eviction request for the given pod name
func createBasicEvictionAdmissionReview(podName, namespace string) admissionv1.AdmissionReview {
	return admissionv1.AdmissionReview{
		Request: &admissionv1.AdmissionRequest{
			UID: "test-request-uid",
			Kind: metav1.GroupVersionKind{
				Group:   "policy",
				Version: "v1",
				Kind:    "Eviction",
			},
			Resource: metav1.GroupVersionResource{
				Group:    "policy",
				Version:  "v1",
				Resource: "evictions",
			},
			Name:      podName,
			Namespace: namespace,
			Operation: admissionv1.Create,
			UserInfo: authenticationv1.UserInfo{
				Username: "test-user",
			},
			SubResource: "eviction",
			Object: runtime.RawExtension{
				Raw: []byte(fmt.Sprintf(`{
					"apiVersion": "policy/v1",
					"kind": "Eviction",
					"metadata": {
						"name": "%s",
						"namespace": "%s"
					}
				}`, podName, namespace)),
			},
		},
	}
}

// newPodNoOwner returns a minimal Pod structure and will not match to any StatefulSet ownership
// The pod defaults to be in a running state
func newPodNoOwner(name string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			UID:       types.UID(uuid.New().String()),
			Name:      name,
			Namespace: testNamespace,
			Labels:    map[string]string{rolloutconfig.RolloutGroupLabelKey: rolloutGroupValue},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
		},
	}
}

// newPod returns a minimal Pod structure and will match to have the given StatefulSet as it's owner
// The pod defaults to be in a running state, and has a rollout-group label applied
func newPod(name string, sts *appsv1.StatefulSet) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			UID:       types.UID(uuid.New().String()),
			Name:      name,
			Namespace: testNamespace,
			Labels:    map[string]string{nameLabel: sts.Name, rolloutconfig.RolloutGroupLabelKey: rolloutGroupValue},
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: "apps/v1",
					Kind:       "StatefulSet",
					Name:       sts.Name,
					UID:        sts.UID,
					Controller: &[]bool{true}[0],
				},
			},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
		},
	}
}

// newEvictionControllerSts returns a minimal StatefulSet structure and has rollout-group labels set both in the meta-data and the spec selector and spec template
func newEvictionControllerSts(name string) *appsv1.StatefulSet {

	replicas := int32(3)
	return &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: testNamespace,
			UID:       types.UID(uuid.New().String()),
			Labels:    map[string]string{rolloutconfig.RolloutGroupLabelKey: rolloutGroupValue},
		},
		Spec: appsv1.StatefulSetSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{nameLabel: name, rolloutconfig.RolloutGroupLabelKey: rolloutGroupValue},
			},
			Replicas: &replicas,

			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{nameLabel: name, rolloutconfig.RolloutGroupLabelKey: rolloutGroupValue},
				},
			},
		},
		Status: appsv1.StatefulSetStatus{
			Replicas: replicas,
		},
	}
}

// newPDBMaxUnavailable returns a raw custom resource configuration which can be used with the Config
// This configuration has maxUnavailable=X and has a name of the given rollout-group
func newPDBMaxUnavailable(maxUnavailable int, rolloutGroup string) *unstructured.Unstructured {
	return &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": fmt.Sprintf("%s/%s", ZoneAwarePodDisruptionBudgetsSpecGroup, ZoneAwarePodDisruptionBudgetsVersion),
			"kind":       ZoneAwarePodDisruptionBudgetName,
			"metadata": map[string]interface{}{
				"name":      rolloutGroup + "-rollout",
				"namespace": testNamespace,
			},
			"spec": map[string]interface{}{
				FieldMaxUnavailable: int64(maxUnavailable),
				FieldSelector: map[string]interface{}{
					FieldMatchLabels: map[string]interface{}{
						rolloutconfig.RolloutGroupLabelKey: rolloutGroup,
					},
				},
			},
		},
	}
}

// newPDBMaxUnavailablePercent returns a raw custom resource configuration which can be used with the Config
// This configuration has maxUnavailablePercentage=X and has a name of the given rollout-group
func newPDBMaxUnavailablePercent(maxUnavailablePercentage int, rolloutGroup string) *unstructured.Unstructured {
	return &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": fmt.Sprintf("%s/%s", ZoneAwarePodDisruptionBudgetsSpecGroup, ZoneAwarePodDisruptionBudgetsVersion),
			"kind":       ZoneAwarePodDisruptionBudgetName,
			"metadata": map[string]interface{}{
				"name":      rolloutGroup + "-rollout",
				"namespace": testNamespace,
			},
			"spec": map[string]interface{}{
				FieldMaxUnavailablePercentage: int64(maxUnavailablePercentage),
				FieldSelector: map[string]interface{}{
					FieldMatchLabels: map[string]interface{}{
						rolloutconfig.RolloutGroupLabelKey: rolloutGroup,
					},
				},
			},
		},
	}
}

// newPDBMaxUnavailableWithRegex returns a raw custom resource configuration which can be used with the Config
// This configuration has maxUnavailable=X, has a name of the given rollout-group and sets the podNamePartitionRegex
func newPDBMaxUnavailableWithRegex(maxUnavailable int, rolloutGroup string, podNamePartitionRegex string, podNameRegexGroup int64) *unstructured.Unstructured {
	return &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": fmt.Sprintf("%s/%s", ZoneAwarePodDisruptionBudgetsSpecGroup, ZoneAwarePodDisruptionBudgetsVersion),
			"kind":       ZoneAwarePodDisruptionBudgetName,
			"metadata": map[string]interface{}{
				"name":      rolloutGroup + "-rollout",
				"namespace": testNamespace,
			},
			"spec": map[string]interface{}{
				FieldMaxUnavailable: int64(maxUnavailable),
				FieldSelector: map[string]interface{}{
					FieldMatchLabels: map[string]interface{}{
						rolloutconfig.RolloutGroupLabelKey: rolloutGroup,
					},
				},
				FieldPodNamePartitionRegex: podNamePartitionRegex,
				FieldPodNameRegexGroup:     podNameRegexGroup,
			},
		},
	}
}
