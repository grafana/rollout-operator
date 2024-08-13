package controller

import (
	"fmt"
	"time"

	"github.com/go-kit/log"
	"github.com/go-kit/log/level"
	v1 "k8s.io/api/apps/v1"

	"github.com/grafana/rollout-operator/pkg/config"
)

// desiredStsReplicas returns desired replicas for statefulset based on leader-replicas, and minimum time between zone downscales.
func desiredStsReplicas(group string, sts *v1.StatefulSet, all []*v1.StatefulSet, logger log.Logger) (int32, error) {
	followerReplicas := *sts.Spec.Replicas
	leader, err := getLeaderForStatefulSet(sts, all)
	if leader == nil || err != nil {
		return followerReplicas, err
	}

	leaderReplicas := *leader.Spec.Replicas
	leaderReadyReplicas := leader.Status.ReadyReplicas

	logger = log.With(
		logger,
		"follower", sts.GetName(),
		"follower_replicas", followerReplicas,
		"leader", leader.GetName(),
		"leader_replicas", leaderReplicas,
		"leader_ready_replicas", leaderReadyReplicas,
		"group", group,
	)

	if leaderReplicas > followerReplicas {
		// Handle scale-up scenarios
		annotations := sts.GetAnnotations()
		onlyWhenReady, ok := annotations[config.RolloutLeaderReadyAnnotationKey]
		if ok && onlyWhenReady == config.RolloutLeaderReadyAnnotationValue {
			// We only scale up once all of the leader pods are ready. Otherwise we do nothing.
			if leaderReplicas != leaderReadyReplicas {
				level.Debug(logger).Log("msg", "not all leader replicas are ready. Follower replicas not changed.")
				return followerReplicas, nil
			}
		}
		return leaderReplicas, nil
	}

	if leaderReplicas < followerReplicas {
		// Add all relevant key-value pairs before checking if the minimum time between
		// scale downs has elapsed.
		minTimeElapsed, err := minimumTimeHasElapsed(sts, all, logger)
		if err != nil {
			return followerReplicas, err
		}

		// Scale down is only allowed if the minimum amount of time since the
		// last scale down by any statefulset in this group has elapsed.
		if minTimeElapsed {
			return leaderReplicas, nil
		}
	}

	return followerReplicas, nil
}

// getLeaderForStatefulSet returns the stateful set that acts as the leader for the follower statefulset,
// nil if there is no configured leader for the follower, or an error if the configured leader does not exist.
func getLeaderForStatefulSet(follower *v1.StatefulSet, sets []*v1.StatefulSet) (*v1.StatefulSet, error) {
	annotations := follower.GetAnnotations()
	leaderName, ok := annotations[config.RolloutDownscaleLeaderAnnotationKey]
	if !ok {
		return nil, nil
	}

	for _, set := range sets {
		if set.GetName() == leaderName {
			return set, nil
		}
	}

	return nil, fmt.Errorf("could not find leader for %s using %s", follower.GetName(), config.RolloutDownscaleLeaderAnnotationKey)
}

// minimumTimeHasElapsed returns true if at least the configured time has elapsed since another statefulset
// has been downscaled, false otherwise, or an error if the statefulset is not configured correctly to be
// downscaled.
func minimumTimeHasElapsed(follower *v1.StatefulSet, all []*v1.StatefulSet, logger log.Logger) (bool, error) {
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
	rawValue, ok := followerLabels[config.MinTimeBetweenZonesDownscaleLabelKey]
	if !ok {
		return false, fmt.Errorf("missing label %s on follower", config.MinTimeBetweenZonesDownscaleLabelKey)
	}

	minTimeSinceDownscale, err := time.ParseDuration(rawValue)
	if err != nil {
		return false, fmt.Errorf("unable to parse label %s value on follower: %w", config.MinTimeBetweenZonesDownscaleLabelKey, err)
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

// getMostRecentDownscale gets the time of the most recent downscale of any statefulset besides
// the provided follower statefulset or an error if the most recent downscale cannot be determined.
// The zero value for time.Time will be returned there have not been any downscales so far.
func getMostRecentDownscale(follower *v1.StatefulSet, all []*v1.StatefulSet) (time.Time, error) {
	var mostRecent time.Time

	for _, sts := range all {
		// We don't consider the statefulset itself when finding the last downscale time since
		// it's fine to scale a single sts down multiple times. We want to make sure that we aren't
		// in the middle of a scaledown in another zone while changing this one.
		if sts.GetName() == follower.GetName() {
			continue
		}

		annotations := sts.GetAnnotations()
		rawValue, ok := annotations[config.LastDownscaleAnnotationKey]
		if !ok {
			// No last downscale time, skip this statefulset. Maybe we'll find
			// an annotation on another one. If not, we just return the time zero
			// value.
			continue
		}

		lastDownscale, err := time.Parse(time.RFC3339, rawValue)
		if err != nil {
			return time.Time{}, fmt.Errorf("can't parse %v annotation of %s: %w", config.LastDownscaleAnnotationKey, sts.GetName(), err)
		}

		if lastDownscale.After(mostRecent) {
			mostRecent = lastDownscale
		}
	}

	return mostRecent, nil
}
