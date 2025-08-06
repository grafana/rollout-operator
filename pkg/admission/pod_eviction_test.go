package admission

import (
	"context"
	"fmt"
	"strings"
	"testing"

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

	"github.com/grafana/rollout-operator/pkg/config"
	"github.com/grafana/rollout-operator/pkg/zpdb"
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
	testNamespace     = "test-dev-0"
	rolloutGroupValue = "ingester"
	rolloutGroupLabel = config.RolloutGroupLabelKey
	nameLabel         = "name"
)

type testContext struct {
	ctx      context.Context
	logs     *dummyLogger
	client   *fake.Clientset
	request  admissionv1.AdmissionReview
	pdbCache *zpdb.ZpdbCache
	podCache *zpdb.PodEvictionCache
}

// A dummyLogger accumulates log lines into a slice so we can assert that certain logs were recorded
type dummyLogger struct {
	logs []string
}

func newDummyLogger() *dummyLogger {
	return &dummyLogger{}
}

func (d *dummyLogger) Log(keyVals ...interface{}) error {
	var parts []string
	for i := 0; i < len(keyVals); i += 2 {
		key := fmt.Sprint(keyVals[i])
		var val string
		if i+1 < len(keyVals) {
			val = fmt.Sprint(keyVals[i+1])
		} else {
			val = "<missing>"
		}
		parts = append(parts, fmt.Sprintf("%s=%s", key, val))
	}

	d.logs = append(d.logs, strings.Join(parts, " "))
	return nil
}

// assertHasLog ensures there is a log line which contains all the given elements
// Note that we do not care about the ordering of the elements
func (d *dummyLogger) assertHasLog(t *testing.T, elements []string) {

	for _, line := range d.logs {
		fmt.Printf("%s\n", line)
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

func newTestContext(request admissionv1.AdmissionReview, pdbRawConfig *unstructured.Unstructured, objects ...runtime.Object) *testContext {
	testCtx := &testContext{}
	testCtx.ctx = context.Background()
	testCtx.client = fake.NewClientset(objects...)
	testCtx.podCache = zpdb.NewPodEvictionCache()
	testCtx.pdbCache = zpdb.NewZpdbCache()

	if pdbRawConfig != nil {
		_, _ = testCtx.pdbCache.AddOrUpdateRaw(pdbRawConfig)
	}
	testCtx.request = request
	testCtx.logs = newDummyLogger()
	return testCtx
}

func (c *testContext) assertDenyResponse(t *testing.T, reason string, statusCode int) {
	response := PodEviction(context.Background(), c.logs, c.request, c.client, c.pdbCache, c.podCache)
	require.NotNil(t, response.UID)
	require.False(t, response.Allowed)
	require.Equal(t, reason, response.Result.Message)
	require.Equal(t, int32(statusCode), response.Result.Code)
}

func (c *testContext) assertAllowResponse(t *testing.T) {
	response := PodEviction(context.Background(), c.logs, c.request, c.client, c.pdbCache, c.podCache)
	require.NotNil(t, response.UID)
	require.True(t, response.Allowed)
}

func (c *testContext) assertAllowResponseWithWarning(t *testing.T, warning string) {
	response := PodEviction(context.Background(), c.logs, c.request, c.client, c.pdbCache, c.podCache)
	require.NotNil(t, response.UID)
	require.True(t, response.Allowed)
	require.Equal(t, warning, response.Warnings[0])
}

func TestPodEviction_NotCreateEvent(t *testing.T) {

	ar := createBasicEvictionAdmissionReview(testPodZoneA0, testNamespace)
	ar.Request.Operation = admissionv1.Delete
	testCtx := newTestContext(ar, nil)
	testCtx.assertAllowResponseWithWarning(t, "request operation is not create")

	expectedLogEntries := []string{
		"method=admission.PodEviction()",
		"object.name=" + testPodZoneA0,
		"object.namespace=" + testNamespace,
		"object.resource=evictions",
		"request.uid=test-request-uid",
		"msg=pod eviction allowed - not a valid create pod eviction request",
	}
	testCtx.logs.assertHasLog(t, expectedLogEntries)
}

func TestPodEviction_NotEvictionSubResource(t *testing.T) {
	ar := createBasicEvictionAdmissionReview(testPodZoneA0, testNamespace)
	ar.Request.SubResource = "foo"
	testCtx := newTestContext(ar, nil)
	testCtx.assertAllowResponseWithWarning(t, "request SubResource is not eviction")
}

func TestPodEviction_EmptyName(t *testing.T) {
	testCtx := newTestContext(createBasicEvictionAdmissionReview("", testNamespace), nil)
	testCtx.assertAllowResponseWithWarning(t, "namespace or name are not found")
}

func TestPodEviction_PodNotFound(t *testing.T) {
	testCtx := newTestContext(createBasicEvictionAdmissionReview(testPodZoneA0, testNamespace), nil)
	testCtx.assertAllowResponseWithWarning(t, "pods \"ingester-zone-a-0\" not found")
	testCtx.logs.assertHasLog(t, []string{"pod eviction allowed - unable to find pod by name"})
}

func TestPodEviction_PodNotReady(t *testing.T) {
	pod := newPodNoOwner(testPodZoneA0)
	pod.Status.Phase = corev1.PodFailed
	testCtx := newTestContext(createBasicEvictionAdmissionReview(testPodZoneA0, testNamespace), nil, pod)
	testCtx.assertAllowResponseWithWarning(t, "pod is not ready")
	testCtx.logs.assertHasLog(t, []string{"pod eviction allowed - pod is not ready"})
}

func TestPodEviction_PodWithNoOwner(t *testing.T) {
	pod := newPodNoOwner(testPodZoneA0)
	testCtx := newTestContext(createBasicEvictionAdmissionReview(testPodZoneA0, testNamespace), newPDBMaxUnavailable(1, rolloutGroupValue), pod)
	testCtx.assertAllowResponseWithWarning(t, "unable to find a StatefulSet pod owner")
	testCtx.logs.assertHasLog(t, []string{"pod eviction allowed - unable to find pod owner"})
}

func TestPodEviction_UnableToRetrievePdbConfig(t *testing.T) {
	sts := newSts(statefulSetZoneA)
	pod := newPod(testPodZoneA0, sts)

	testCtx := newTestContext(createBasicEvictionAdmissionReview(testPodZoneA0, testNamespace), nil, pod, sts)
	testCtx.assertDenyResponse(t, "no pod disruption budgets found for pod", 500)
}

func TestPodEviction_MaxUnavailableEq0(t *testing.T) {
	sts := newSts(statefulSetZoneA)
	pod := newPod(testPodZoneA0, sts)

	testCtx := newTestContext(createBasicEvictionAdmissionReview(testPodZoneA0, testNamespace), newPDBMaxUnavailable(0, rolloutGroupValue), pod, sts)
	testCtx.assertDenyResponse(t, "max unavailable = 0", 403)
	testCtx.logs.assertHasLog(t, []string{"msg=pod eviction denied - max unavailable = 0"})
}

func TestPodEviction_MaxUnavailablePercentageEq0(t *testing.T) {
	sts := newSts(statefulSetZoneA)
	pod := newPod(testPodZoneA0, sts)

	testCtx := newTestContext(createBasicEvictionAdmissionReview(testPodZoneA0, testNamespace), newPDBMaxUnavailablePercent(0, rolloutGroupValue), pod, sts)
	testCtx.assertDenyResponse(t, "max unavailable = 0", 403)
	testCtx.logs.assertHasLog(t, []string{"msg=pod eviction denied - max unavailable = 0"})
}

// TestPodEviction_SingleZoneSinglePod - a single zone with a single pod becoming unavailable
func TestPodEviction_SingleZoneSinglePod(t *testing.T) {
	replicas := int32(1)
	sts := newSts(statefulSetZoneA)
	sts.Spec.Replicas = &replicas
	sts.Status.Replicas = replicas
	pod := newPod(testPodZoneA0, sts)

	testCtx := newTestContext(createBasicEvictionAdmissionReview(testPodZoneA0, testNamespace), newPDBMaxUnavailable(1, rolloutGroupValue), pod, sts)
	testCtx.assertAllowResponse(t)
}

// TestPodEviction_SingleZoneMultiplePods - a single zone with 1 pod not ready
func TestPodEviction_SingleZoneMultiplePods(t *testing.T) {
	sts := newSts(statefulSetZoneA)
	pod0 := newPod(testPodZoneA0, sts)
	pod1 := newPod(testPodZoneA1, sts)
	pod2 := newPod(testPodZoneA2, sts)

	testCtx := newTestContext(createBasicEvictionAdmissionReview(testPodZoneA0, testNamespace), newPDBMaxUnavailable(1, rolloutGroupValue), pod0, pod1, pod2, sts)
	testCtx.assertAllowResponse(t)

	pod2.Status.Phase = corev1.PodPending
	testCtx = newTestContext(createBasicEvictionAdmissionReview(testPodZoneA0, testNamespace), newPDBMaxUnavailable(1, rolloutGroupValue), pod0, pod1, pod2, sts)
	testCtx.assertDenyResponse(t, "1 pod not ready under ingester-zone-a", 429)
}

// TestPodEviction_SingleZoneMultiplePodsUpscale - the StatefulSet spec replica is reporting more than we see reported when listing the pods
func TestPodEviction_SingleZoneMultiplePodsUpscale(t *testing.T) {
	sts := newSts(statefulSetZoneA)
	pod0 := newPod(testPodZoneA0, sts)
	pod1 := newPod(testPodZoneA1, sts)

	testCtx := newTestContext(createBasicEvictionAdmissionReview(testPodZoneA0, testNamespace), newPDBMaxUnavailable(1, rolloutGroupValue), pod0, pod1, sts)
	testCtx.assertDenyResponse(t, "1 pod unknown under ingester-zone-a", 429)
}

func TestPodEviction_MultiZoneClassic(t *testing.T) {

	objs := make([]runtime.Object, 12)
	objs[0] = newSts(statefulSetZoneA)
	objs[1] = newSts(statefulSetZoneB)
	objs[2] = newSts(statefulSetZoneC)

	idx := 3
	for _, p := range []string{testPodZoneA0, testPodZoneA1, testPodZoneA2} {
		objs[idx] = newPod(p, objs[0].(*appsv1.StatefulSet))
		idx++
	}
	for _, p := range []string{testPodZoneB0, testPodZoneB1, testPodZoneB2} {
		objs[idx] = newPod(p, objs[1].(*appsv1.StatefulSet))
		idx++
	}
	for _, p := range []string{testPodZoneC0, testPodZoneC1, testPodZoneC2} {
		objs[idx] = newPod(p, objs[2].(*appsv1.StatefulSet))
		idx++
	}

	testCtx := newTestContext(createBasicEvictionAdmissionReview(testPodZoneA0, testNamespace), newPDBMaxUnavailable(1, rolloutGroupValue), objs...)
	require.False(t, testCtx.podCache.Evicted(objs[3].(*corev1.Pod)))
	testCtx.assertAllowResponse(t)
	require.True(t, testCtx.podCache.Evicted(objs[3].(*corev1.Pod)))

	// mark a pod in the same zone as failed - we will allow this eviction
	objs[5].(*corev1.Pod).Status.Phase = corev1.PodFailed
	testCtx = newTestContext(createBasicEvictionAdmissionReview(testPodZoneA0, testNamespace), newPDBMaxUnavailable(1, rolloutGroupValue), objs...)
	testCtx.assertAllowResponse(t)

	// mark a pod in the another zone as failed - we will deny this eviction
	objs[11].(*corev1.Pod).Status.Phase = corev1.PodFailed
	testCtx = newTestContext(createBasicEvictionAdmissionReview(testPodZoneA0, testNamespace), newPDBMaxUnavailable(1, rolloutGroupValue), objs...)
	testCtx.assertDenyResponse(t, "1 pod not ready", 429)
	testCtx.logs.assertHasLog(t, []string{"msg=pod eviction denied - zpdb exceeded"})

	// reset so all the pods are reporting running
	objs[5].(*corev1.Pod).Status.Phase = corev1.PodRunning
	objs[11].(*corev1.Pod).Status.Phase = corev1.PodRunning

	// but zone b sts has more replicas than we will see pods for
	objs[1].(*appsv1.StatefulSet).Status.Replicas = 4
	testCtx = newTestContext(createBasicEvictionAdmissionReview(testPodZoneA0, testNamespace), newPDBMaxUnavailable(1, rolloutGroupValue), objs...)
	testCtx.assertDenyResponse(t, "1 pod unknown", 429)
	testCtx.logs.assertHasLog(t, []string{"msg=pod eviction denied - zpdb exceeded"})
}

func TestPodEviction_MultiZoneClassicMaxUnavailable2(t *testing.T) {

	objs := make([]runtime.Object, 12)
	objs[0] = newSts(statefulSetZoneA)
	objs[1] = newSts(statefulSetZoneB)
	objs[2] = newSts(statefulSetZoneC)

	idx := 3
	for _, p := range []string{testPodZoneA0, testPodZoneA1, testPodZoneA2} {
		objs[idx] = newPod(p, objs[0].(*appsv1.StatefulSet))
		idx++
	}
	for _, p := range []string{testPodZoneB0, testPodZoneB1, testPodZoneB2} {
		objs[idx] = newPod(p, objs[1].(*appsv1.StatefulSet))
		idx++
	}
	for _, p := range []string{testPodZoneC0, testPodZoneC1, testPodZoneC2} {
		objs[idx] = newPod(p, objs[2].(*appsv1.StatefulSet))
		idx++
	}

	testCtx := newTestContext(createBasicEvictionAdmissionReview(testPodZoneA0, testNamespace), newPDBMaxUnavailable(2, rolloutGroupValue), objs...)
	testCtx.assertAllowResponse(t)

	// mark a pod in the same zone as failed - we will allow this eviction as we can have 2 pods unavailable
	objs[5].(*corev1.Pod).Status.Phase = corev1.PodFailed
	testCtx = newTestContext(createBasicEvictionAdmissionReview(testPodZoneA0, testNamespace), newPDBMaxUnavailable(2, rolloutGroupValue), objs...)
	testCtx.assertAllowResponse(t)

	// mark another pod in the same zone as failed - we will allow this eviction
	objs[4].(*corev1.Pod).Status.Phase = corev1.PodFailed
	testCtx = newTestContext(createBasicEvictionAdmissionReview(testPodZoneA0, testNamespace), newPDBMaxUnavailable(2, rolloutGroupValue), objs...)
	testCtx.assertAllowResponse(t)

	// mark a pod in the another zone as failed - we have 2 pods offline in our zone and one pod offline in another zone
	objs[11].(*corev1.Pod).Status.Phase = corev1.PodFailed
	testCtx = newTestContext(createBasicEvictionAdmissionReview(testPodZoneA0, testNamespace), newPDBMaxUnavailable(2, rolloutGroupValue), objs...)
	testCtx.assertAllowResponse(t)

	// mark a pod in the last zone as failed - we have 2 pods offline in our zone and a pod offline in both other zones
	objs[6].(*corev1.Pod).Status.Phase = corev1.PodFailed
	testCtx = newTestContext(createBasicEvictionAdmissionReview(testPodZoneA0, testNamespace), newPDBMaxUnavailable(2, rolloutGroupValue), objs...)
	testCtx.assertDenyResponse(t, "2 pods not ready", 429)
}

func TestPodEviction_PartitionZones(t *testing.T) {

	objs := make([]runtime.Object, 12)
	objs[0] = newSts(statefulSetZoneA)
	objs[1] = newSts(statefulSetZoneB)
	objs[2] = newSts(statefulSetZoneC)

	idx := 3
	for _, p := range []string{testPodZoneA0, testPodZoneA1, testPodZoneA2} {
		objs[idx] = newPod(p, objs[0].(*appsv1.StatefulSet))
		idx++
	}
	for _, p := range []string{testPodZoneB0, testPodZoneB1, testPodZoneB2} {
		objs[idx] = newPod(p, objs[1].(*appsv1.StatefulSet))
		idx++
	}
	for _, p := range []string{testPodZoneC0, testPodZoneC1, testPodZoneC2} {
		objs[idx] = newPod(p, objs[2].(*appsv1.StatefulSet))
		idx++
	}

	// ingester-zone-a-0 --> 0
	podPartitionZoneRegex := "[a-z\\-]+-zone-[a-z]-([0-9]+)"

	testCtx := newTestContext(createBasicEvictionAdmissionReview(testPodZoneA0, testNamespace), newPDBMaxUnavailableWithRegex(1, rolloutGroupValue, podPartitionZoneRegex), objs...)
	testCtx.assertAllowResponse(t)

	// mark a pod in the same zone as failed - we will allow this eviction
	objs[4].(*corev1.Pod).Status.Phase = corev1.PodFailed
	testCtx = newTestContext(createBasicEvictionAdmissionReview(testPodZoneA0, testNamespace), newPDBMaxUnavailableWithRegex(1, rolloutGroupValue, podPartitionZoneRegex), objs...)
	testCtx.assertAllowResponse(t)

	// mark a pod in the another zone + partition as failed - we will allow this eviction
	objs[11].(*corev1.Pod).Status.Phase = corev1.PodFailed
	testCtx = newTestContext(createBasicEvictionAdmissionReview(testPodZoneA0, testNamespace), newPDBMaxUnavailableWithRegex(1, rolloutGroupValue, podPartitionZoneRegex), objs...)
	testCtx.assertAllowResponse(t)

	// mark a pod in the another zone + same partition as failed - we will deny this eviction
	objs[6].(*corev1.Pod).Status.Phase = corev1.PodFailed
	testCtx = newTestContext(createBasicEvictionAdmissionReview(testPodZoneA0, testNamespace), newPDBMaxUnavailableWithRegex(1, rolloutGroupValue, podPartitionZoneRegex), objs...)
	testCtx.assertDenyResponse(t, "1 pod not ready under partition 0", 429)

	// reset so all the pods are reporting running
	objs[4].(*corev1.Pod).Status.Phase = corev1.PodRunning
	objs[6].(*corev1.Pod).Status.Phase = corev1.PodRunning
	objs[11].(*corev1.Pod).Status.Phase = corev1.PodRunning

	// but zone b sts has more replicas than we will see pods for
	objs[1].(*appsv1.StatefulSet).Status.Replicas = 4
	testCtx = newTestContext(createBasicEvictionAdmissionReview(testPodZoneA0, testNamespace), newPDBMaxUnavailableWithRegex(1, rolloutGroupValue, podPartitionZoneRegex), objs...)
	testCtx.assertDenyResponse(t, "1 pod unknown under partition 0", 429)

	testCtx = newTestContext(createBasicEvictionAdmissionReview(testPodZoneA0, testNamespace), newPDBMaxUnavailableWithRegex(1, rolloutGroupValue, "ingester(-foo)?-zone-[a-z]-([0-9]+),$2"), objs...)
	testCtx.assertDenyResponse(t, "1 pod unknown under partition 0", 429)
}

func TestPodEviction_PartitionZonesMaxUnavailable2(t *testing.T) {

	objs := make([]runtime.Object, 12)
	objs[0] = newSts(statefulSetZoneA)
	objs[1] = newSts(statefulSetZoneB)
	objs[2] = newSts(statefulSetZoneC)

	idx := 3
	for _, p := range []string{testPodZoneA0, testPodZoneA1, testPodZoneA2} {
		objs[idx] = newPod(p, objs[0].(*appsv1.StatefulSet))
		idx++
	}
	for _, p := range []string{testPodZoneB0, testPodZoneB1, testPodZoneB2} {
		objs[idx] = newPod(p, objs[1].(*appsv1.StatefulSet))
		idx++
	}
	for _, p := range []string{testPodZoneC0, testPodZoneC1, testPodZoneC2} {
		objs[idx] = newPod(p, objs[2].(*appsv1.StatefulSet))
		idx++
	}

	// ingester-zone-a-0 --> 0
	podPartitionZoneRegex := "[a-z\\-]+-zone-[a-z]-([0-9]+)"

	// mark 1 pod in the another zone + same partition as failed
	objs[6].(*corev1.Pod).Status.Phase = corev1.PodFailed
	testCtx := newTestContext(createBasicEvictionAdmissionReview(testPodZoneA0, testNamespace), newPDBMaxUnavailableWithRegex(1, rolloutGroupValue, podPartitionZoneRegex), objs...)
	testCtx.assertDenyResponse(t, "1 pod not ready under partition 0", 429)

	testCtx = newTestContext(createBasicEvictionAdmissionReview(testPodZoneA0, testNamespace), newPDBMaxUnavailableWithRegex(2, rolloutGroupValue, podPartitionZoneRegex), objs...)
	testCtx.assertAllowResponse(t)

	// mark another pod in the another zone + same partition as failed
	objs[9].(*corev1.Pod).Status.Phase = corev1.PodFailed
	testCtx = newTestContext(createBasicEvictionAdmissionReview(testPodZoneA0, testNamespace), newPDBMaxUnavailableWithRegex(2, rolloutGroupValue, podPartitionZoneRegex), objs...)
	testCtx.assertDenyResponse(t, "2 pods not ready under partition 0", 429)
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
			Labels:    map[string]string{rolloutGroupLabel: rolloutGroupValue},
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
			Labels:    map[string]string{nameLabel: sts.Name, rolloutGroupLabel: rolloutGroupValue},
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

// newSts returns a minimal StatefulSet structure and has rollout-group labels set both in the meta-data and the spec selector and spec template
func newSts(name string) *appsv1.StatefulSet {

	replicas := int32(3)
	return &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: testNamespace,
			UID:       types.UID(uuid.New().String()),
			Labels:    map[string]string{rolloutGroupLabel: rolloutGroupValue},
		},
		Spec: appsv1.StatefulSetSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{nameLabel: name, rolloutGroupLabel: rolloutGroupValue},
			},
			Replicas: &replicas,

			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{nameLabel: name, rolloutGroupLabel: rolloutGroupValue},
				},
			},
		},
		Status: appsv1.StatefulSetStatus{
			Replicas: replicas,
		},
	}
}

// newPDBMaxUnavailable returns a raw custom resource configuration which can be used with the ZpdbConfig
// This configuration has maxUnavailable=X and has a name of the given rollout-group
func newPDBMaxUnavailable(maxUnavailable int, rolloutGroup string) *unstructured.Unstructured {
	return &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "rollout-operator.grafana.com/v1",
			"kind":       "ZoneAwarePodDisruptionBudget",
			"metadata": map[string]interface{}{
				"name":      rolloutGroup + "-rollout",
				"namespace": testNamespace,
			},
			"spec": map[string]interface{}{
				"maxUnavailable": int64(maxUnavailable),
				"selector": map[string]interface{}{
					"matchLabels": map[string]interface{}{
						rolloutGroupLabel: rolloutGroup,
					},
				},
			},
		},
	}
}

// newPDBMaxUnavailablePercent returns a raw custom resource configuration which can be used with the ZpdbConfig
// This configuration has maxUnavailablePercentage=X and has a name of the given rollout-group
func newPDBMaxUnavailablePercent(maxUnavailablePercentage int, rolloutGroup string) *unstructured.Unstructured {
	return &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "rollout-operator.grafana.com/v1",
			"kind":       "ZoneAwarePodDisruptionBudget",
			"metadata": map[string]interface{}{
				"name":      rolloutGroup + "-rollout",
				"namespace": testNamespace,
			},
			"spec": map[string]interface{}{
				"maxUnavailablePercentage": int64(maxUnavailablePercentage),
				"selector": map[string]interface{}{
					"matchLabels": map[string]interface{}{
						rolloutGroupLabel: rolloutGroup,
					},
				},
			},
		},
	}
}

// newPDBMaxUnavailableWithRegex returns a raw custom resource configuration which can be used with the ZpdbConfig
// This configuration has maxUnavailable=X, has a name of the given rollout-group and sets the podNamePartitionRegex
func newPDBMaxUnavailableWithRegex(maxUnavailable int, rolloutGroup string, podNamePartitionRegex string) *unstructured.Unstructured {
	return &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "rollout-operator.grafana.com/v1",
			"kind":       "ZoneAwarePodDisruptionBudget",
			"metadata": map[string]interface{}{
				"name":      rolloutGroup + "-rollout",
				"namespace": testNamespace,
			},
			"spec": map[string]interface{}{
				"maxUnavailable": int64(maxUnavailable),
				"selector": map[string]interface{}{
					"matchLabels": map[string]interface{}{
						rolloutGroupLabel: rolloutGroup,
					},
				},
				"podNamePartitionRegex": podNamePartitionRegex,
			},
		},
	}
}
