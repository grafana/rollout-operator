package zpdb

import (
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
)

// A Validator allows for different pdb implementations
type Validator interface {

	// ConsiderSts returns true if this StatefulSet should be considered in the PDB tallies
	ConsiderSts(sts *appsv1.StatefulSet) bool

	// ConsiderPod returns a matcher which is used to test if a pod should be considered in the PDB tallies
	ConsiderPod() *PartitionMatcher

	// AccumulateResult is called for each StatefulSet which is tested
	AccumulateResult(sts *appsv1.StatefulSet, result *ZoneStatusResult) error

	// Validate is called after all the StatefulSets have been tested - this function validates that the PDB will not be breached
	Validate(maxUnavailable int) error

	// SuccessMessage returns a success message we return in the eviction response
	SuccessMessage() string
}

// A PartitionMatcher is a utility to assist in matching pods to a partition.
type PartitionMatcher struct {
	// Same returns true if this pod is in the same zone/partition as the eviction pod
	Same func(pod *corev1.Pod) bool
}

// A ZoneStatusResult holds the status of a pod availability within a zone / StatefulSet
type ZoneStatusResult struct {
	// the number of pods tested for their status
	Tested int
	// the number of pods who are not ready/running
	NotReady int
	// the number of pods we do not know their status
	Unknown int
}

// plural appends an 's' to the given string if the value is > 1.
func plural(s string, value int) string {
	if value > 1 {
		return fmt.Sprintf("%ss", s)
	}
	return s
}

// pdbMessage creates a message which includes the number of not ready and unknown pods within the given span
func pdbMessage(result *ZoneStatusResult, span string) string {
	msg := ""
	if result.NotReady > 0 {
		// 1 pod not ready
		msg += fmt.Sprintf("%d %s not ready", result.NotReady, plural("pod", result.NotReady))
		if result.Unknown > 0 {
			msg += ", "
		}
	}
	if result.Unknown > 0 {
		// 2 pods unknown
		msg += fmt.Sprintf("%d %s unknown", result.Unknown, plural("pod", result.Unknown))
	}

	// under ingester-zone-a partition 0
	// under ingester-zone-a
	if len(span) > 0 {
		msg += fmt.Sprintf(" under %s", span)
	}
	return msg
}
