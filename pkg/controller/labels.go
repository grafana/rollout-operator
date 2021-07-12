package controller

import (
	"fmt"
	"strconv"

	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
	v1 "k8s.io/api/apps/v1"
)

const (
	RolloutGroupLabel               = "rollout-group"
	RolloutMaxUnavailableAnnotation = "rollout-max-unavailable"
)

// getMaxUnavailableForStatefulSet returns the number of max unavailable pods configured
// via the annotation on the provided StatefulSet.
func getMaxUnavailableForStatefulSet(sts *v1.StatefulSet, logger log.Logger) int {
	annotations := sts.GetAnnotations()
	rawValue, ok := annotations[RolloutMaxUnavailableAnnotation]

	if !ok {
		// No parallel rollout by default.
		return 1
	}

	value, err := strconv.Atoi(rawValue)
	if err != nil || value <= 0 {
		level.Error(logger).Log(
			"msg", fmt.Sprintf("StatefulSet has invalid %s annotation (expected positive integer)", RolloutMaxUnavailableAnnotation),
			"statefulset", sts.Name,
			"value", rawValue)

		return 1
	}

	return value
}
