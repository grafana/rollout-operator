package zpdb

import (
	"errors"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
)

type validatorZoneAware struct {
	sts     *appsv1.StatefulSet
	result  *zoneStatusResult
	zones   int
	matcher partitionMatcher
}

func newValidatorZoneAware(sts *appsv1.StatefulSet, zones int) *validatorZoneAware {
	return &validatorZoneAware{
		sts:   sts,
		zones: zones,
		matcher: func(pod *corev1.Pod) bool {
			return true
		},
	}
}

func (v *validatorZoneAware) considerSts(_ *appsv1.StatefulSet) bool {
	return true
}

func (v *validatorZoneAware) accumulateResult(otherSts *appsv1.StatefulSet, r *zoneStatusResult) error {
	if otherSts.UID == v.sts.UID {
		v.result = r
	} else {
		// fail fast - there is a disruption in another zone
		if r.notReady+r.unknown > 0 {
			return errors.New(pdbMessage(r, otherSts.Name))
		}
	}
	return nil
}

func (v *validatorZoneAware) validate(maxUnavailable int) error {
	if v.result.notReady+v.result.unknown >= maxUnavailable {
		return errors.New(pdbMessage(v.result, v.sts.Name))
	}
	return nil
}

func (v *validatorZoneAware) successMessage() string {
	return fmt.Sprintf("zpdb met across %d zones", v.zones)
}

func (v *validatorZoneAware) considerPod() partitionMatcher {
	return v.matcher
}
