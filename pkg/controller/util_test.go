package controller

import (
	"testing"

	"github.com/stretchr/testify/assert"
	v1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestSortStatefulSets(t *testing.T) {
	input := []*v1.StatefulSet{
		{ObjectMeta: metav1.ObjectMeta{Name: "ingester-zone-b"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "ingester-zone-a"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "ingester-zone-c"}},
	}

	expected := []*v1.StatefulSet{
		{ObjectMeta: metav1.ObjectMeta{Name: "ingester-zone-a"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "ingester-zone-b"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "ingester-zone-c"}},
	}

	sortStatefulSets(input)
	assert.Equal(t, expected, input)
}

func TestSortPods(t *testing.T) {
	input := []*corev1.Pod{
		{ObjectMeta: metav1.ObjectMeta{Name: "ingester-11"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "ingester-0"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "ingester-10"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "ingester-20"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "ingester-1"}},
	}

	expected := []*corev1.Pod{
		{ObjectMeta: metav1.ObjectMeta{Name: "ingester-0"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "ingester-1"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "ingester-10"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "ingester-11"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "ingester-20"}},
	}

	sortPods(input)
	assert.Equal(t, expected, input)
}

func TestPodNames(t *testing.T) {
	input := []*corev1.Pod{
		{ObjectMeta: metav1.ObjectMeta{Name: "ingester-11"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "ingester-0"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "ingester-10"}},
	}

	assert.Equal(t, []string{"ingester-11", "ingester-0", "ingester-10"}, podNames(input))
}

func TestIsPodRunningAndReady(t *testing.T) {
	tests := map[string]struct {
		pod      *corev1.Pod
		expected bool
	}{
		"should return true on a ready pod with 1 ready and running container": {
			pod: &corev1.Pod{Status: corev1.PodStatus{
				Phase: corev1.PodRunning,
				ContainerStatuses: []corev1.ContainerStatus{
					{Ready: true, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}},
				},
			}},
			expected: true,
		},
		"should return true on a ready pod with multiple ready and running containers": {
			pod: &corev1.Pod{Status: corev1.PodStatus{
				Phase: corev1.PodRunning,
				ContainerStatuses: []corev1.ContainerStatus{
					{Ready: true, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}},
					{Ready: true, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}},
					{Ready: true, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}},
				},
			}},
			expected: true,
		},
		"should return false if a container is not ready": {
			pod: &corev1.Pod{Status: corev1.PodStatus{
				Phase: corev1.PodRunning,
				ContainerStatuses: []corev1.ContainerStatus{
					{Ready: true, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}},
					{Ready: true, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}},
					{Ready: false, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}},
				},
			}},
			expected: false,
		},
		"should return false if a container is terminated": {
			pod: &corev1.Pod{Status: corev1.PodStatus{
				Phase: corev1.PodRunning,
				ContainerStatuses: []corev1.ContainerStatus{
					{Ready: true, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}},
					{Ready: true, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}},
					{Ready: true, State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{}}},
				},
			}},
			expected: false,
		},
		"should return false if the pod is not in the running phase": {
			pod: &corev1.Pod{Status: corev1.PodStatus{
				Phase: corev1.PodFailed,
				ContainerStatuses: []corev1.ContainerStatus{
					{Ready: true, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}},
					{Ready: true, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}},
					{Ready: true, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}},
				},
			}},
			expected: false,
		},
		"should return false if the pod is terminating": {
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					DeletionTimestamp: now(),
				},
				Status: corev1.PodStatus{
					Phase: corev1.PodRunning,
					ContainerStatuses: []corev1.ContainerStatus{
						{Ready: true, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}},
						{Ready: true, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}},
						{Ready: true, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}},
					},
				},
			},
			expected: false,
		},
	}

	for testName, testData := range tests {
		t.Run(testName, func(t *testing.T) {
			assert.Equal(t, testData.expected, isPodRunningAndReady(testData.pod))
		})
	}
}

func TestMoveStatefulSetToFront(t *testing.T) {
	input := []*v1.StatefulSet{
		{ObjectMeta: metav1.ObjectMeta{Name: "ingester-zone-a"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "ingester-zone-b"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "ingester-zone-c"}},
	}

	expected := []*v1.StatefulSet{
		{ObjectMeta: metav1.ObjectMeta{Name: "ingester-zone-b"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "ingester-zone-a"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "ingester-zone-c"}},
	}

	actual := moveStatefulSetToFront(input, input[1])
	assert.Equal(t, expected, actual)
}

func TestGroupStatefulSetsByLabel(t *testing.T) {
	input := []*v1.StatefulSet{
		{ObjectMeta: metav1.ObjectMeta{Name: "ingester-zone-a", Labels: map[string]string{RolloutGroupLabel: "ingester"}}},
		{ObjectMeta: metav1.ObjectMeta{Name: "ingester-zone-b", Labels: map[string]string{RolloutGroupLabel: "ingester"}}},
		{ObjectMeta: metav1.ObjectMeta{Name: "compactor-zone-a", Labels: map[string]string{RolloutGroupLabel: "compactor"}}},
		{ObjectMeta: metav1.ObjectMeta{Name: "compactor-zone-b", Labels: map[string]string{RolloutGroupLabel: "compactor"}}},
		{ObjectMeta: metav1.ObjectMeta{Name: "store-gateway"}},
	}

	expected := map[string][]*v1.StatefulSet{
		"ingester": {
			{ObjectMeta: metav1.ObjectMeta{Name: "ingester-zone-a", Labels: map[string]string{RolloutGroupLabel: "ingester"}}},
			{ObjectMeta: metav1.ObjectMeta{Name: "ingester-zone-b", Labels: map[string]string{RolloutGroupLabel: "ingester"}}},
		},
		"compactor": {
			{ObjectMeta: metav1.ObjectMeta{Name: "compactor-zone-a", Labels: map[string]string{RolloutGroupLabel: "compactor"}}},
			{ObjectMeta: metav1.ObjectMeta{Name: "compactor-zone-b", Labels: map[string]string{RolloutGroupLabel: "compactor"}}},
		},
		"": {
			{ObjectMeta: metav1.ObjectMeta{Name: "store-gateway"}},
		},
	}

	assert.Equal(t, expected, groupStatefulSetsByLabel(input, RolloutGroupLabel))
}

func TestMax(t *testing.T) {
	assert.Equal(t, 1, max(1))
	assert.Equal(t, 3, max(0, 3, 2))
}

func TestMin(t *testing.T) {
	assert.Equal(t, 1, min(1))
	assert.Equal(t, 3, min(4, 3, 5))
}

func now() *metav1.Time {
	ts := metav1.Now()
	return &ts
}
