package controller

import (
	"sort"

	"github.com/facette/natsort"
	v1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
)

// sortStatefulSets sorts in-place the provided slice of StatefulSet.
func sortStatefulSets(sets []*v1.StatefulSet) {
	sort.Slice(sets, func(i, j int) bool {
		return sets[i].Name < sets[j].Name
	})
}

// sortPods sorts in-place the provided slice of Pod. Pod names are sorted
// in natural order in order to play nicely with StatefulSet naming schema.
func sortPods(pods []*corev1.Pod) {
	sort.Slice(pods, func(i, j int) bool {
		return natsort.Compare(pods[i].Name, pods[j].Name)
	})
}

// podNames returns the names of input pods.
func podNames(pods []*corev1.Pod) []string {
	names := make([]string, 0, len(pods))
	for _, pod := range pods {
		names = append(names, pod.Name)
	}
	return names
}

// isPodRunningAndReady returns whether the input pod is running and ready.
func isPodRunningAndReady(pod *corev1.Pod) bool {
	// The pod phase must be "running".
	if pod.Status.Phase != corev1.PodRunning {
		return false
	}

	// It must not be in the terminating state.
	if pod.DeletionTimestamp != nil {
		return false
	}

	// All containers must be running and ready.
	for i := 0; i < len(pod.Status.ContainerStatuses); i++ {
		container := pod.Status.ContainerStatuses[i]

		if !container.Ready || container.State.Running == nil {
			return false
		}
	}

	return true
}

// moveStatefulSetToFront returns a new slice where the input StatefulSet toMove is moved
// to the beginning. Comparison is done via pointer equality.
func moveStatefulSetToFront(sets []*v1.StatefulSet, toMove *v1.StatefulSet) []*v1.StatefulSet {
	out := make([]*v1.StatefulSet, 0, len(sets))
	out = append(out, toMove)

	for _, set := range sets {
		if set != toMove {
			out = append(out, set)
		}
	}

	return out
}

// groupStatefulSetsByLabel returns a map containing the input StatefulSets grouped by
// the input label's value.
func groupStatefulSetsByLabel(sets []*v1.StatefulSet, label string) map[string][]*v1.StatefulSet {
	groups := make(map[string][]*v1.StatefulSet)

	for _, sts := range sets {
		value := sts.GetLabels()[label]
		groups[value] = append(groups[value], sts)
	}

	return groups
}

// mustNewLabelsRequirement wraps labels.NewRequirement() and panics on error.
// This utility function can be safely used whenever the input is deterministic
// (eg. based on hard-coded config).
func mustNewLabelsRequirement(key string, op selection.Operator, vals []string) labels.Requirement {
	req, err := labels.NewRequirement(key, op, vals)
	if err != nil {
		panic(err)
	}
	return *req
}

func max(value int, others ...int) int {
	for _, other := range others {
		if other > value {
			value = other
		}
	}

	return value
}

func min(value int, others ...int) int {
	for _, other := range others {
		if other < value {
			value = other
		}
	}

	return value
}
