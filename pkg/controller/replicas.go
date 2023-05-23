package controller

import (
	"context"
	"fmt"
	"time"

	"github.com/go-kit/log"
	"github.com/go-kit/log/level"
	v1 "k8s.io/api/apps/v1"

	"github.com/grafana/rollout-operator/pkg/admission"
)

type ScaleAction int

const (
	ScaleUp ScaleAction = iota
	ScaleDown
	NoChange
)

type replicaChanges struct {
	action   ScaleAction
	replicas int32
}

func reconcileStsReplicas(ctx context.Context, sts *v1.StatefulSet, all []*v1.StatefulSet, logger log.Logger) (*replicaChanges, error) {
	return nil, nil
}

func getLeaderForStatefulSet(sts *v1.StatefulSet, sets []*v1.StatefulSet) *v1.StatefulSet {
	annotations := sts.GetAnnotations()
	leaderName, ok := annotations[RolloutDownscaleLeaderAnnotation]
	if !ok {
		return nil
	}

	for _, set := range sets {
		if set.GetName() == leaderName {
			return set
		}
	}

	return nil
}

func minimumTimeHasPassed(follower *v1.StatefulSet, leader *v1.StatefulSet, logger log.Logger) (bool, error) {

	// TODO: Do we need to check all statefulsets here? Same logic as the webhook?

	leaderAnnotations := leader.GetAnnotations()
	rawValue, ok := leaderAnnotations[admission.LastDownscaleAnnotationKey]
	if !ok {
		// No last downscale for the leader. Should be fine to adjust the follower
		// replica count to match.
		return true, nil
	}

	leaderLastDownscale, err := time.Parse(time.RFC3339, rawValue)
	if err != nil {
		return false, fmt.Errorf("unable to parse annotation %s value on leader: %w", admission.LastDownscaleAnnotationKey, err)
	}

	followerLabels := follower.GetLabels()
	rawValue, ok = followerLabels[admission.MinTimeBetweenZonesDownscaleLabelKey]
	if !ok {
		return false, fmt.Errorf("missing label %s on follower", admission.MinTimeBetweenZonesDownscaleLabelKey)
	}

	minTimeSinceDownscale, err := time.ParseDuration(rawValue)
	if err != nil {
		return false, fmt.Errorf("unable to parse label %s value on follower: %w", admission.MinTimeBetweenZonesDownscaleLabelKey, err)
	}

	timeSinceDownscale := time.Since(leaderLastDownscale)

	level.Debug(logger).Log(
		"msg", "determined last downscale time",
		"last_downscale", leaderLastDownscale.String(),
		"time_since_downscale", timeSinceDownscale.String(),
		"min_time_since_downscale", minTimeSinceDownscale.String(),
	)

	return timeSinceDownscale > minTimeSinceDownscale, nil

}
