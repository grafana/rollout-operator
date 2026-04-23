package zpdb

import (
	"errors"
	"fmt"
	"time"

	"github.com/go-kit/log"
	"github.com/go-kit/log/level"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"

	"github.com/grafana/rollout-operator/pkg/util"
)

type validatorZoneAware struct {
	sts           *appsv1.StatefulSet
	result        *zoneStatusResult
	zones         int
	matcher       partitionMatcher
	pdbConfig     *config
	evictionCache *podEvictionCache
	readyCache    *podReadinessCache
	logger        log.Logger
}

func newValidatorZoneAware(sts *appsv1.StatefulSet, zones int, evictionCache *podEvictionCache, readyCache *podReadinessCache, pdbConfig *config, logger log.Logger) *validatorZoneAware {
	return &validatorZoneAware{
		sts:   sts,
		zones: zones,
		matcher: func(pod *corev1.Pod) bool {
			return true
		},
		evictionCache: evictionCache,
		readyCache:    readyCache,
		pdbConfig:     pdbConfig,
		logger:        logger,
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

func (v *validatorZoneAware) isReady(pod *corev1.Pod) bool {

	// This pod has been recently evicted or is not in a ready + running state
	if v.evictionCache.hasPendingEviction(pod) || !util.IsPodRunningAndReady(pod) {
		return false
	}

	readyRecord, ok := v.readyCache.get(pod)
	if !ok {
		// We should always expect there to be a cached value since the readyCache is populated from the pod_observer and the process
		// starting up waits for the informer caches have been synced before progressing.
		// We return true since the pod is reporting ready + running we just can not verify the eviction delay.
		level.Error(v.logger).Log("msg", "No ready cache record for pod - cross zone eviction delay can not be enforced", "pod", pod.Name)
		return true
	}
	if !readyRecord.evicted {
		// We do not have any history for this pod being evicted and then recovering.
		// evicted will be false when the rollout-operator first starts, and it has not observed any eviction lifecycles.
		// As such we need to consider this pod as ready since we do not have the history of when it transitioned to ready/running.
		level.Info(v.logger).Log("msg", "No eviction record in ready cache for pod - cross zone eviction delay can not be enforced", "pod", pod.Name)
		return true
	}

	// Ensure that enough time has elapsed since this pod became ready
	// Why do we check readyRunning again? This avoids a race between this test being run
	// and a pod being observed as changing to a ready/running state. We need to ensure
	// we are using the time since becoming ready and not a time since becoming not ready.
	// It is possible that util.IsPodRunningAndReady() returns true but the readyCache has the pod not ready.
	// The cached record will be updated once the pod observer notifies the readyCache of the update.
	if readyRecord.readyRunning && time.Now().After(readyRecord.since.Add(v.pdbConfig.crossZoneEvictionDelay)) {
		return true
	}

	level.Info(v.logger).Log("msg", "Pod not considered ready - not enough time has elapsed since this pod became ready", "pod", pod.Name)
	return false
}
