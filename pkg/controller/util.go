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

// TODO document
func moveStatefulSetToFirst(sets []*v1.StatefulSet, first *v1.StatefulSet) []*v1.StatefulSet {
	out := make([]*v1.StatefulSet, 0, len(sets)+1)
	out = append(out, first)

	for _, set := range sets {
		if set != first {
			out = append(out, set)
		}
	}

	return out
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
