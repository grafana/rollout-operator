package zpdb

import (
	"errors"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
)

type ValidatorZoneAware struct {
	sts     *appsv1.StatefulSet
	result  *ZoneStatusResult
	zones   int
	matcher *PartitionMatcher
}

func NewValidatorZoneAware(sts *appsv1.StatefulSet, zones int) *ValidatorZoneAware {
	return &ValidatorZoneAware{
		sts:   sts,
		zones: zones,
		matcher: &PartitionMatcher{
			Same: func(pod *corev1.Pod) bool {
				return true
			},
		},
	}
}

func (v *ValidatorZoneAware) ConsiderSts(_ *appsv1.StatefulSet) bool {
	return true
}

func (v *ValidatorZoneAware) AccumulateResult(otherSts *appsv1.StatefulSet, r *ZoneStatusResult) error {
	if otherSts.UID == v.sts.UID {
		v.result = r
	} else {
		// fail fast - there is a disruption in another zone
		if r.NotReady+r.Unknown > 0 {
			return errors.New(pdbMessage(r, otherSts.Name))
		}
	}
	return nil
}

func (v *ValidatorZoneAware) Validate(maxUnavailable int) error {
	if v.result.NotReady+v.result.Unknown+1 > maxUnavailable {
		return errors.New(pdbMessage(v.result, v.sts.Name))
	}
	return nil
}

func (v *ValidatorZoneAware) SuccessMessage() string {
	return fmt.Sprintf("zpdb met across %d zones", v.zones)
}

func (v *ValidatorZoneAware) ConsiderPod() *PartitionMatcher {
	return v.matcher
}
