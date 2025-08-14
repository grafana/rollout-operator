package zpdb

import (
	"errors"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
)

// A ValidatorSingleZone is an implementation of Validator.
// This is a zone aware pdb which applies in a single zone topology.
// The pdb unavailability test applies to all pods found within a single statefulset / zone.
type ValidatorSingleZone struct {
	sts     *appsv1.StatefulSet
	result  *ZoneStatusResult
	matcher PartitionMatcher
}

func NewValidatorSingleZone(sts *appsv1.StatefulSet) *ValidatorSingleZone {
	return &ValidatorSingleZone{
		sts: sts,
		matcher: func(pod *corev1.Pod) bool {
			return true
		},
	}
}

func (p *ValidatorSingleZone) ConsiderSts(_ *appsv1.StatefulSet) bool {
	return true
}

func (p *ValidatorSingleZone) SuccessMessage() string {
	return fmt.Sprintf("zpdb met in single zone %s", p.sts.Name)
}

func (p *ValidatorSingleZone) AccumulateResult(sts *appsv1.StatefulSet, r *ZoneStatusResult) error {
	p.result = r
	return nil
}

func (p *ValidatorSingleZone) Validate(maxUnavailable int) error {
	// add 1 to reflect the pod which is being requested for eviction
	if p.result.NotReady+p.result.Unknown >= maxUnavailable {
		return errors.New(pdbMessage(p.result, p.sts.Name))
	}
	return nil
}

func (p *ValidatorSingleZone) ConsiderPod() PartitionMatcher {
	return p.matcher
}
