package controller

import (
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

func reconcileStsReplicas(group string, sts *v1.StatefulSet, all []*v1.StatefulSet, logger log.Logger) (replicaChanges, error) {
	followerReplicas := *sts.Spec.Replicas
	noReplicaChanges := replicaChanges{action: NoChange, replicas: followerReplicas}

	leader, err := getLeaderForStatefulSet(sts, all)
	if leader == nil || err != nil {
		return noReplicaChanges, err
	}

	leaderReplicas := *leader.Spec.Replicas
	if leaderReplicas > followerReplicas {
		// Scale up is always allowed immediately
		return replicaChanges{action: ScaleUp, replicas: leaderReplicas}, nil
	}

	if leaderReplicas < followerReplicas {
		// Add all relevant key-value pairs before checking if the minimum time between
		// scale downs has elapsed.
		logger = log.With(
			logger,
			"follower", sts.GetName(),
			"follower_replicas", followerReplicas,
			"leader", leader.GetName(),
			"leader_replicas", leaderReplicas,
			"group", group,
		)

		minTimeElapsed, err := minimumTimeHasPassed(sts, all, logger)
		if err != nil {
			return noReplicaChanges, err
		}

		// Scale down is only allowed if the minimum amount of time since the
		// last scale down by a statefulset's designated leader has elapsed.
		if minTimeElapsed {
			return replicaChanges{action: ScaleDown, replicas: leaderReplicas}, nil
		}
	}

	return noReplicaChanges, nil
}

func getLeaderForStatefulSet(sts *v1.StatefulSet, sets []*v1.StatefulSet) (*v1.StatefulSet, error) {
	annotations := sts.GetAnnotations()
	leaderName, ok := annotations[RolloutDownscaleLeaderAnnotation]
	if !ok {
		return nil, nil
	}

	for _, set := range sets {
		if set.GetName() == leaderName {
			return set, nil
		}
	}

	return nil, fmt.Errorf("could not find leader for %s using %s", sts.GetName(), RolloutDownscaleLeaderAnnotation)
}

func minimumTimeHasPassed(follower *v1.StatefulSet, all []*v1.StatefulSet, logger log.Logger) (bool, error) {
	lastDownscale, err := getMostRecentDownscale(follower, all)
	if err != nil {
		return false, err
	}

	if lastDownscale.IsZero() {
		// No last downscale for any sts. Should be fine to adjust the follower
		// replica count to whatever the leader replica count is.
		level.Debug(logger).Log("msg", "no last downscale time for any other sts found")
		return true, nil
	}

	followerLabels := follower.GetLabels()
	rawValue, ok := followerLabels[admission.MinTimeBetweenZonesDownscaleLabelKey]
	if !ok {
		return false, fmt.Errorf("missing label %s on follower", admission.MinTimeBetweenZonesDownscaleLabelKey)
	}

	minTimeSinceDownscale, err := time.ParseDuration(rawValue)
	if err != nil {
		return false, fmt.Errorf("unable to parse label %s value on follower: %w", admission.MinTimeBetweenZonesDownscaleLabelKey, err)
	}

	timeSinceDownscale := time.Since(lastDownscale)
	level.Debug(logger).Log(
		"msg", "determined last downscale time",
		"last_downscale", lastDownscale.String(),
		"time_since_downscale", timeSinceDownscale.String(),
		"min_time_since_downscale", minTimeSinceDownscale.String(),
	)

	return timeSinceDownscale > minTimeSinceDownscale, nil

}

func getMostRecentDownscale(follower *v1.StatefulSet, all []*v1.StatefulSet) (time.Time, error) {
	var mostRecent time.Time

	for _, sts := range all {
		// We don't consider a statefulset itself when finding the last downscale time since it's
		// fine to scale a single sts down multiple times. We want to make sure that we aren't in
		// the middle of a scaledown in another zone while changing this one.
		if sts.GetName() == follower.GetName() {
			continue
		}

		annotations := sts.GetAnnotations()
		rawValue, ok := annotations[admission.LastDownscaleAnnotationKey]
		if !ok {
			// No last downscale time, skip this statefulset. Maybe we'll find
			// an annotation on another one. If not, we just return the time zero
			// value.
			continue
		}

		lastDownscale, err := time.Parse(time.RFC3339, rawValue)
		if err != nil {
			return time.Time{}, fmt.Errorf("can't parse %v annotation of %s: %w", admission.LastDownscaleAnnotationKey, sts.GetName(), err)
		}

		if lastDownscale.After(mostRecent) {
			mostRecent = lastDownscale
		}
	}

	return mostRecent, nil
}
