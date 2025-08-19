package zpdb

import (
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
)

// A validator allows for different pdb implementations
type validator interface {

	// considerSts returns true if this StatefulSet should be considered in the PDB tallies
	considerSts(sts *appsv1.StatefulSet) bool

	// considerPod returns a matcher which is used to test if a pod should be considered in the PDB tallies
	considerPod() partitionMatcher

	// accumulateResult is called for each StatefulSet which is tested
	accumulateResult(sts *appsv1.StatefulSet, result *zoneStatusResult) error

	// validate is called after all the StatefulSets have been tested - this function validates that the PDB will not be breached
	validate(maxUnavailable int) error

	// successMessage returns a success message we return in the eviction response
	successMessage() string
}

// A partitionMatcher is a utility to assist in matching pods to a partition.
// Returns true if this pod is in the same zone/partition as the eviction pod
type partitionMatcher func(pod *corev1.Pod) bool

// A zoneStatusResult holds the status of a pod availability within a zone / StatefulSet
type zoneStatusResult struct {
	// the number of pods tested for their status
	tested int
	// the number of pods who are not ready/running
	notReady int
	// the number of pods we do not know their status
	unknown int
}

// plural appends an 's' to the given string if the value is > 1.
func plural(s string, value int) string {
	if value > 1 {
		return fmt.Sprintf("%ss", s)
	}
	return s
}

// pdbMessage creates a message which includes the number of not ready and unknown pods within the given span
func pdbMessage(result *zoneStatusResult, span string) string {
	msg := ""
	if result.notReady > 0 {
		// 1 pod not ready
		msg += fmt.Sprintf("%d %s not ready", result.notReady, plural("pod", result.notReady))
		if result.unknown > 0 {
			msg += ", "
		}
	}
	if result.unknown > 0 {
		// 2 pods unknown
		msg += fmt.Sprintf("%d %s unknown", result.unknown, plural("pod", result.unknown))
	}

	// in ingester-zone-a partition 0
	// in ingester-zone-a
	if len(span) > 0 {
		msg += fmt.Sprintf(" in %s", span)
	}
	return msg
}
