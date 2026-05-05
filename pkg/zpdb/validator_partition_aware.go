package zpdb

import (
	"errors"
	"fmt"
	"time"

	"github.com/go-kit/log/level"
	"github.com/grafana/dskit/spanlogger"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"

	"github.com/grafana/rollout-operator/pkg/util"
)

type validatorPartitionAware struct {
	sts           *appsv1.StatefulSet
	result        *zoneStatusResult
	partition     string
	matcher       partitionMatcher
	zones         int
	pdbConfig     *config
	evictionCache *podEvictionCache
	readyTracker  *podReadinessTracker
	log           *spanlogger.SpanLogger
}

func newValidatorPartitionAware(sts *appsv1.StatefulSet, partition string, zones int, pdbConfig *config, evictionCache *podEvictionCache, readyTracker *podReadinessTracker, log *spanlogger.SpanLogger) *validatorPartitionAware {
	partitionMatcher := func(pd *corev1.Pod) bool {
		thisPartition, err := pdbConfig.podPartition(pd)
		if err != nil {
			// the partition name was successfully extracted from the pod being evicted
			// so if this regex has failed then the assumption is that it is not the same partition, as would have a different naming convention
			// or the regex is too tightly defined
			level.Error(log).Log("msg", "Unable to extract partition from pod name - check the pod partition name regex", "name", pd.Name)
		}
		return thisPartition == partition
	}

	return &validatorPartitionAware{
		sts:           sts,
		partition:     partition,
		zones:         zones,
		pdbConfig:     pdbConfig,
		evictionCache: evictionCache,
		readyTracker:  readyTracker,
		log:           log,
		result:        &zoneStatusResult{},
		matcher:       partitionMatcher,
	}
}

func (v *validatorPartitionAware) considerSts(otherSts *appsv1.StatefulSet) bool {
	return otherSts.UID != v.sts.UID
}

func (v *validatorPartitionAware) accumulateResult(sts *appsv1.StatefulSet, r *zoneStatusResult) error {
	v.result.tested += r.tested
	v.result.notReady += r.notReady
	v.result.unknown += r.unknown

	// If we were unable to confirm a pod status in this zone we should assume the result is unknown
	// Note that this assumes that we only expect 1 pod per zone per partition.
	// If ever this assumption changed, we would need to increment unknown by the number of expected pods per zone per partition.
	if r.tested == 0 && r.notReady == 0 && r.unknown == 0 {
		level.Debug(v.log).Log("msg", "No pod test result for %s. Assuming pod state is unknown", sts.Name)
		v.result.unknown++
	}

	return nil
}

func (v *validatorPartitionAware) validate(maxUnavailable int) error {
	if v.result.notReady+v.result.unknown >= maxUnavailable {
		return errors.New(pdbMessage(v.result, "partition "+v.partition))
	}
	return nil
}

func (v *validatorPartitionAware) successMessage() string {
	return fmt.Sprintf("zpdb met for partition %s across %d zones", v.partition, v.zones)
}

func (v *validatorPartitionAware) considerPod() partitionMatcher {
	return v.matcher
}

func (v *validatorPartitionAware) isReady(pod *corev1.Pod) bool {

	// This pod has been recently evicted or is not in a ready + running state
	if v.evictionCache.hasPendingEviction(pod) || !util.IsPodRunningAndReady(pod) {
		return false
	}

	// No cross-zone eviction delay configured - any ready+running pod counts as ready.
	if v.pdbConfig.crossZoneEvictionDelay == 0 {
		return true
	}

	// Delay configured: the pod is only ready once enough time has elapsed since it last
	// transitioned to ready+running. The transition time is read from the pod's
	// podReadyAnnotationKey annotation (or time.Now() when the annotation is absent or invalid,
	// which is the safe default that denies eviction until the delay window has been observed).
	now := time.Now()
	since := v.readyTracker.get(pod)
	if now.After(since.Add(v.pdbConfig.crossZoneEvictionDelay)) {
		return true
	}

	level.Info(v.log).Log("msg", "Pod not considered ready - not enough time has elapsed since this pod became ready", "pod", pod.Name, "time-until-ready", since.Add(v.pdbConfig.crossZoneEvictionDelay).Sub(now))
	return false
}
