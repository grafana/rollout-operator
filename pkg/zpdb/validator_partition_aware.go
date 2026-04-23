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
	readyCache    *podReadinessCache
	log           *spanlogger.SpanLogger
}

func newValidatorPartitionAware(sts *appsv1.StatefulSet, partition string, zones int, pdbConfig *config, evictionCache *podEvictionCache, readyCache *podReadinessCache, log *spanlogger.SpanLogger) *validatorPartitionAware {
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
		readyCache:    readyCache,
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

	level.Info(v.log).Log("msg", "Testing if pod is ready", "pod", pod.Name, "delay", v.pdbConfig.crossZoneEvictionDelay)

	// No change to existing zpdb logic where this value is not set
	if v.pdbConfig.crossZoneEvictionDelay == 0 {
		return true
	}

	readyRecord, ok := v.readyCache.get(pod)
	if !ok {
		// We should always expect there to be a cached value since the readyCache is populated from the pod_observer and the process
		// starting up waits for the informer caches have been synced before progressing.
		// We return true since the pod is reporting ready + running - we just can not verify the eviction delay.
		level.Error(v.log).Log("msg", "No ready cache record for pod - cross zone eviction delay can not be enforced", "pod", pod.Name)
		return true
	}
	if !readyRecord.evicted {
		// We do not have any history for this pod being evicted and then recovering.
		// evicted will be false when the rollout-operator first starts, and it has not observed any eviction lifecycles.
		// As such we need to consider this pod as ready since we do not have the history of when it transitioned to ready/running.
		// This is ok from the zpdb perspective as the usual flow would be;
		// * pods (a,b) in each zone for a given partition are ready + running - all have readyRecord.evicted=false
		// * pod a is requested to be evicted
		// * pod b is tested here. It will be allowed since b is ready+running and we do not have the history to know when it last reached ready
		// * pod a is evicted
		// * whilst pod a is restarting it will not pass this ready test, so pod b can not be evicted
		// * pod a returns to ready+running. If pod b is tested for eviction, pod a will be tested here and fail since not enough time has elapsed
		// * pod b can not be evicted until pod a's time in ready elapses
		level.Info(v.log).Log("msg", "No eviction record in ready cache for pod - cross zone eviction delay can not be enforced", "pod", pod.Name)
		return true
	}

	now := time.Now()
	// Ensure that enough time has elapsed since this pod became ready
	// Why do we check readyRunning again? This avoids a race between this test being run
	// and a pod being observed as changing to a ready/running state. We need to ensure
	// we are using the time since becoming ready and not a time since becoming not ready.
	// It is possible that util.IsPodRunningAndReady() returns true but the readyCache has the pod not ready.
	// The cached record will be updated once the pod observer notifies the readyCache of the update.
	if readyRecord.readyRunning && now.After(readyRecord.since.Add(v.pdbConfig.crossZoneEvictionDelay)) {

		return true
	}

	level.Info(v.log).Log("msg", "Pod not considered ready - not enough time has elapsed since this pod became ready", "pod", pod.Name, "time-until-ready", readyRecord.since.Add(v.pdbConfig.crossZoneEvictionDelay).Sub(now))
	return false
}
