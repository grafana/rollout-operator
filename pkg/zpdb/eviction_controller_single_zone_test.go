package zpdb

import (
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"

	rolloutconfig "github.com/grafana/rollout-operator/pkg/config"
)

func TestPodEviction_SingleZoneWindowAfterDownscale(t *testing.T) {
	const (
		smallReplicaCount = 5
		largeReplicaCount = 10
		partitionToEvict  = 7 // This partition exists in Zone B but was deleted from Zone A
	)

	// Create StatefulSets for two zones
	// Zone A has completed downscale (5 replicas)
	stsZoneA := newStatefulSetWithReplicas(statefulSetZoneA, smallReplicaCount)
	// Zone B hasn't started downscale yet (10 replicas)
	stsZoneB := newStatefulSetWithReplicas(statefulSetZoneB, largeReplicaCount)

	objs := make([]runtime.Object, 0)
	objs = append(objs, stsZoneA, stsZoneB)

	// Create pods for Zone A (0-4 only, since it scaled down)
	for i := 0; i < smallReplicaCount; i++ {
		podName := fmt.Sprintf("%s-%d", statefulSetZoneA, i)
		objs = append(objs, newPodWithIndex(podName, stsZoneA, i))
	}

	// Create pods for Zone B (0-9, full replica set)
	var podToEvict *corev1.Pod
	for i := 0; i < largeReplicaCount; i++ {
		podName := fmt.Sprintf("%s-%d", statefulSetZoneB, i)
		pod := newPodWithIndex(podName, stsZoneB, i)
		if i == partitionToEvict {
			podToEvict = pod
		}
		objs = append(objs, pod)
	}

	podPartitionRegex := "[a-z\\-]+-zone-[a-z]-([0-9]+)"
	pdb := newPDBMaxUnavailableWithRegex(1, rolloutGroupValue, podPartitionRegex, int64(1))

	// Try to evict zone-b-7
	// This partition no longer exists in Zone A (was deleted during downscale)
	require.NotNil(t, podToEvict, "Pod to evict should have been created")
	testCtx := newTestContext(t, createBasicEvictionAdmissionReview(podToEvict.Name, testNamespace), pdb, objs...)
	defer testCtx.controller.Stop()

	response := testCtx.controller.HandlePodEvictionRequest(testCtx.ctx, testCtx.request, NewMaxUnavailableZeroOverrideNone())
	require.NotNil(t, response.UID)
	require.False(t, response.Allowed) // Currently fails
}

// newStatefulSetWithReplicas creates a StatefulSet with the specified replica count
func newStatefulSetWithReplicas(name string, replicas int) *appsv1.StatefulSet {
	replicas32 := int32(replicas)
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
			Replicas: &replicas32,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{nameLabel: name, rolloutconfig.RolloutGroupLabelKey: rolloutGroupValue},
				},
			},
		},
		Status: appsv1.StatefulSetStatus{
			Replicas: replicas32,
		},
	}
}

// newPodWithIndex creates a pod with the specified index for testing
func newPodWithIndex(name string, sts *appsv1.StatefulSet, index int) *corev1.Pod {
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
			Conditions: []corev1.PodCondition{
				{
					Type:   corev1.PodReady,
					Status: corev1.ConditionTrue,
				},
			},
		},
	}
}
