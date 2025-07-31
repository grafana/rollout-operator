package admission

import (
	"context"
	"fmt"
	"github.com/google/uuid"
	"github.com/grafana/dskit/spanlogger"
	"github.com/grafana/rollout-operator/pkg/config"
	"github.com/stretchr/testify/require"
	admissionv1 "k8s.io/api/admission/v1"
	appsv1 "k8s.io/api/apps/v1"
	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	fakedynamic "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	"strings"
	"testing"
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
	ctx     context.Context
	logs    *dummyLogger
	client  *fake.Clientset
	dynamic *fakedynamic.FakeDynamicClient
	request admissionv1.AdmissionReview
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
	if pdbRawConfig != nil {
		testCtx.dynamic = fakedynamic.NewSimpleDynamicClient(runtime.NewScheme(), pdbRawConfig)
	} else {
		testCtx.dynamic = fakedynamic.NewSimpleDynamicClient(runtime.NewScheme())
	}

	testCtx.request = request
	testCtx.logs = newDummyLogger()
	return testCtx
}

// assertResponse asserts that the pod eviction resource is as expected.
func (c *testContext) assertResponse(t *testing.T, allowed bool, reason string) {
	response := PodEviction(context.Background(), c.logs, c.request, c.client, c.dynamic)

	// Verify response
	require.Equal(t, allowed, response.Allowed)
	require.Equal(t, reason, response.Warnings[0])
}

func (c *testContext) newSpanLogger() *spanlogger.SpanLogger {
	logger, _ := spanlogger.New(c.ctx, c.logs, "admission.PodEviction()", tenantResolver)
	return logger
}

func TestPodEviction_NotCreateEvent(t *testing.T) {

	ar := createBasicEvictionAdmissionReview(testPodZoneA0, testNamespace)
	ar.Request.Operation = admissionv1.Delete
	testCtx := newTestContext(ar, nil)
	testCtx.assertResponse(t, true, "request operation is not create")

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
	testCtx.assertResponse(t, true, "request SubResource is not eviction")
}

func TestPodEviction_EmptyName(t *testing.T) {
	testCtx := newTestContext(createBasicEvictionAdmissionReview("", testNamespace), nil)
	testCtx.assertResponse(t, true, "namespace or name are not found")
}

func TestPodEviction_PodNotFound(t *testing.T) {
	testCtx := newTestContext(createBasicEvictionAdmissionReview(testPodZoneA0, testNamespace), nil)
	testCtx.assertResponse(t, true, "pods \"ingester-zone-a-0\" not found")
	testCtx.logs.assertHasLog(t, []string{"pod eviction allowed - unable to find pod by name"})
}

func TestPodEviction_PodNotReady(t *testing.T) {
	pod := newPodNoOwner(testPodZoneA0)
	pod.Status.Phase = corev1.PodFailed
	testCtx := newTestContext(createBasicEvictionAdmissionReview(testPodZoneA0, testNamespace), nil, pod)
	testCtx.assertResponse(t, true, "pod is not ready")
	testCtx.logs.assertHasLog(t, []string{"pod eviction allowed - pod is not ready"})
}

func TestPodEviction_PodWithNoOwner(t *testing.T) {
	pod := newPodNoOwner(testPodZoneA0)
	testCtx := newTestContext(createBasicEvictionAdmissionReview(testPodZoneA0, testNamespace), nil, pod)
	testCtx.assertResponse(t, true, "unable to find a StatefulSet as pod owner")
	testCtx.logs.assertHasLog(t, []string{"pod eviction allowed - unable to find pod owner"})
}

func TestPodEviction_MissingRolloutGroupLabelOnPod(t *testing.T) {
	pod := newPodNoOwner(testPodZoneA0)
	delete(pod.Labels, config.RolloutGroupLabelKey)

	testCtx := newTestContext(createBasicEvictionAdmissionReview(testPodZoneA0, testNamespace), nil, pod)
	testCtx.assertResponse(t, true, "unable to find label on pod")
	testCtx.logs.assertHasLog(t, []string{"msg=pod eviction allowed - unable to find required label on pod", "label=rollout-group"})
}

func TestPodEviction_UnableToRetrievePdbConfig(t *testing.T) {
	sts := newSts(statefulSetZoneA)
	pod := newPod(testPodZoneA0, sts)

	testCtx := newTestContext(createBasicEvictionAdmissionReview(testPodZoneA0, testNamespace), nil, pod, sts)
	testCtx.assertResponse(t, true, "unable to load PodDisruptionZoneBudget config")
	testCtx.logs.assertHasLog(t, []string{"msg=PodDisruptionZoneBudget configuration error", "reason=poddisruptionzonebudgets.rollout-operator.grafana.com \"ingester\" not found"})
}

func TestPodEviction_MaxUnavailableEq0(t *testing.T) {
	sts := newSts(statefulSetZoneA)
	pod := newPod(testPodZoneA0, sts)

	testCtx := newTestContext(createBasicEvictionAdmissionReview(testPodZoneA0, testNamespace), newPodDisruptionBudgetMaxUnavailable(0, rolloutGroupValue), pod, sts)
	testCtx.assertResponse(t, false, "max unavailable = 0")
	testCtx.logs.assertHasLog(t, []string{"msg=pod eviction denied - max unavailable = 0"})
}

func TestPodEviction_MaxUnavailablePercentageEq0(t *testing.T) {
	sts := newSts(statefulSetZoneA)
	pod := newPod(testPodZoneA0, sts)

	testCtx := newTestContext(createBasicEvictionAdmissionReview(testPodZoneA0, testNamespace), newPodDisruptionBudgetMaxUnavailablePercent(0, rolloutGroupValue), pod, sts)
	testCtx.assertResponse(t, false, "max unavailable = 0")
	testCtx.logs.assertHasLog(t, []string{"msg=pod eviction denied - max unavailable = 0"})
}

func TestPodEviction_SingleZoneSinglePod(t *testing.T) {
	sts := newSts(statefulSetZoneA)
	pod := newPod(testPodZoneA0, sts)

	testCtx := newTestContext(createBasicEvictionAdmissionReview(testPodZoneA0, testNamespace), newPodDisruptionBudgetMaxUnavailable(1, rolloutGroupValue), pod, sts)
	testCtx.assertResponse(t, true, "all relevant pods in adjacent zones are available")
}

func TestPodEviction_SingleZoneMultiplePods(t *testing.T) {
	sts := newSts(statefulSetZoneA)
	pod := newPod(testPodZoneA0, sts)
	anotherPod := newPod(testPodZoneA1, sts)

	testCtx := newTestContext(createBasicEvictionAdmissionReview(testPodZoneA0, testNamespace), newPodDisruptionBudgetMaxUnavailable(1, rolloutGroupValue), pod, anotherPod, sts)
	testCtx.assertResponse(t, true, "all relevant pods in adjacent zones are available")

	anotherPod.Status.Phase = corev1.PodPending
	testCtx = newTestContext(createBasicEvictionAdmissionReview(testPodZoneA0, testNamespace), newPodDisruptionBudgetMaxUnavailable(1, rolloutGroupValue), pod, anotherPod, sts)
	testCtx.assertResponse(t, false, "1 pod not ready under ingester-zone-a")
	testCtx.logs.assertHasLog(t, []string{"msg=pod eviction denied - pdb exceeded", "sts=ingester-zone-a", "notReady=1", "maxUnavailable=1", "tested=2"})
}

func TestPodEviction_SingleZoneMultiplePodsStartingUp(t *testing.T) {

	sts := newSts(statefulSetZoneA)
	sts.Status.Replicas = 3

	pod := newPod(testPodZoneA0, sts)
	anotherPod := newPod(testPodZoneA1, sts)

	testCtx := newTestContext(createBasicEvictionAdmissionReview(testPodZoneA0, testNamespace), newPodDisruptionBudgetMaxUnavailable(1, rolloutGroupValue), pod, anotherPod, sts)
	testCtx.assertResponse(t, false, "1 pod unknown under ingester-zone-a")
	testCtx.logs.assertHasLog(t, []string{"msg=pod eviction denied - pdb exceeded", "sts=ingester-zone-a", "notReady=0", "maxUnavailable=1", "tested=2", "unknown=1"})
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

	testCtx := newTestContext(createBasicEvictionAdmissionReview(testPodZoneA0, testNamespace), newPodDisruptionBudgetMaxUnavailable(1, rolloutGroupValue), objs...)
	testCtx.assertResponse(t, true, "all relevant pods in adjacent zones are available")

	// mark a pod in the same zone as failed - we will allow this eviction
	objs[5].(*corev1.Pod).Status.Phase = corev1.PodFailed
	testCtx = newTestContext(createBasicEvictionAdmissionReview(testPodZoneA0, testNamespace), newPodDisruptionBudgetMaxUnavailable(1, rolloutGroupValue), objs...)
	testCtx.assertResponse(t, true, "all relevant pods in adjacent zones are available")

	// mark a pod in the another zone as failed - we will deny this eviction
	objs[11].(*corev1.Pod).Status.Phase = corev1.PodFailed
	testCtx = newTestContext(createBasicEvictionAdmissionReview(testPodZoneA0, testNamespace), newPodDisruptionBudgetMaxUnavailable(1, rolloutGroupValue), objs...)
	testCtx.assertResponse(t, false, "1 pod not ready under ingester-zone-c")
	testCtx.logs.assertHasLog(t, []string{"msg=pod eviction denied - pdb exceeded"})

	// reset so all the pods are reporting running
	objs[5].(*corev1.Pod).Status.Phase = corev1.PodRunning
	objs[11].(*corev1.Pod).Status.Phase = corev1.PodRunning

	// but zone b sts has more replicas than we will see pods for
	objs[1].(*appsv1.StatefulSet).Status.Replicas = 4
	testCtx = newTestContext(createBasicEvictionAdmissionReview(testPodZoneA0, testNamespace), newPodDisruptionBudgetMaxUnavailable(1, rolloutGroupValue), objs...)
	testCtx.assertResponse(t, false, "1 pod unknown under ingester-zone-b")
	testCtx.logs.assertHasLog(t, []string{"msg=pod eviction denied - pdb exceeded"})
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

	testCtx := newTestContext(createBasicEvictionAdmissionReview(testPodZoneA0, testNamespace), newPodDisruptionBudgetMaxUnavailableWithRegex(1, rolloutGroupValue, podPartitionZoneRegex), objs...)
	testCtx.assertResponse(t, true, "all relevant pods in adjacent zones partition 0 are available")

	// mark a pod in the same zone as failed - we will allow this eviction
	objs[4].(*corev1.Pod).Status.Phase = corev1.PodFailed
	testCtx = newTestContext(createBasicEvictionAdmissionReview(testPodZoneA0, testNamespace), newPodDisruptionBudgetMaxUnavailableWithRegex(1, rolloutGroupValue, podPartitionZoneRegex), objs...)
	testCtx.assertResponse(t, true, "all relevant pods in adjacent zones partition 0 are available")

	// mark a pod in the another zone + partition as failed - we will allow this eviction
	objs[11].(*corev1.Pod).Status.Phase = corev1.PodFailed
	testCtx = newTestContext(createBasicEvictionAdmissionReview(testPodZoneA0, testNamespace), newPodDisruptionBudgetMaxUnavailableWithRegex(1, rolloutGroupValue, podPartitionZoneRegex), objs...)
	testCtx.assertResponse(t, true, "all relevant pods in adjacent zones partition 0 are available")

	// mark a pod in the another zone + same partition as failed - we will deny this eviction
	objs[6].(*corev1.Pod).Status.Phase = corev1.PodFailed
	testCtx = newTestContext(createBasicEvictionAdmissionReview(testPodZoneA0, testNamespace), newPodDisruptionBudgetMaxUnavailableWithRegex(1, rolloutGroupValue, podPartitionZoneRegex), objs...)
	testCtx.assertResponse(t, false, "1 pod not ready under ingester-zone-b partition 0")

	// reset so all the pods are reporting running
	objs[4].(*corev1.Pod).Status.Phase = corev1.PodRunning
	objs[6].(*corev1.Pod).Status.Phase = corev1.PodRunning
	objs[11].(*corev1.Pod).Status.Phase = corev1.PodRunning

	// but zone b sts has more replicas than we will see pods for
	objs[1].(*appsv1.StatefulSet).Status.Replicas = 4
	testCtx = newTestContext(createBasicEvictionAdmissionReview(testPodZoneA0, testNamespace), newPodDisruptionBudgetMaxUnavailableWithRegex(1, rolloutGroupValue, podPartitionZoneRegex), objs...)
	testCtx.assertResponse(t, false, "1 pod unknown under ingester-zone-b")
	testCtx.logs.assertHasLog(t, []string{"msg=pod eviction denied - pdb exceeded", "unknown=1"})
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
	}
}

// newPodDisruptionBudgetMaxUnavailable returns a raw custom resource configuration which can be used with the config.PdbConfig
// This configuration has maxUnavailable=X and has a name of the given rollout-group
func newPodDisruptionBudgetMaxUnavailable(maxUnavailable int, rolloutGroup string) *unstructured.Unstructured {
	return &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "rollout-operator.grafana.com/v1",
			"kind":       "PodDisruptionZoneBudget",
			"metadata": map[string]interface{}{
				"name":      rolloutGroup,
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

// newPodDisruptionBudgetMaxUnavailable returns a raw custom resource configuration which can be used with the config.PdbConfig
// This configuration has maxUnavailablePercentage=X and has a name of the given rollout-group
func newPodDisruptionBudgetMaxUnavailablePercent(maxUnavailablePercentage int, rolloutGroup string) *unstructured.Unstructured {
	return &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "rollout-operator.grafana.com/v1",
			"kind":       "PodDisruptionZoneBudget",
			"metadata": map[string]interface{}{
				"name":      rolloutGroup,
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

// newPodDisruptionBudgetMaxUnavailable returns a raw custom resource configuration which can be used with the config.PdbConfig
// This configuration has maxUnavailable=X, has a name of the given rollout-group and sets the podNamePartitionRegex
func newPodDisruptionBudgetMaxUnavailableWithRegex(maxUnavailable int, rolloutGroup string, podNamePartitionRegex string) *unstructured.Unstructured {
	return &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "rollout-operator.grafana.com/v1",
			"kind":       "PodDisruptionZoneBudget",
			"metadata": map[string]interface{}{
				"name":      rolloutGroup,
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
