package controller

import (
	"fmt"
	"strconv"

	"github.com/go-kit/log"
	"github.com/go-kit/log/level"
	v1 "k8s.io/api/apps/v1"

	"github.com/grafana/rollout-operator/pkg/config"
)

// getMaxUnavailableForStatefulSet returns the number of max unavailable pods configured
// via the annotation on the provided StatefulSet.
func getMaxUnavailableForStatefulSet(sts *v1.StatefulSet, logger log.Logger) int {
	annotations := sts.GetAnnotations()
	rawValue, ok := annotations[config.RolloutMaxUnavailableAnnotationKey]
	if !ok {
		// No parallel rollout by default.
		return 1
	}

	value, err := strconv.Atoi(rawValue)
	if err != nil || value <= 0 {
		level.Error(logger).Log(
			"msg", fmt.Sprintf("StatefulSet has invalid %s annotation (expected positive integer)", config.RolloutMaxUnavailableAnnotationKey),
			"statefulset", sts.Name,
			"value", rawValue)

		return 1
	}

	return value
}
