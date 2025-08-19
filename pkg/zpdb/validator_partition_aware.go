package zpdb

import (
	"errors"
	"fmt"

	"github.com/go-kit/log/level"
	"github.com/grafana/dskit/spanlogger"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
)

type validatorPartitionAware struct {
	sts       *appsv1.StatefulSet
	result    *zoneStatusResult
	partition string
	matcher   partitionMatcher
	zones     int
	pdbConfig *config
	log       *spanlogger.SpanLogger
}

func newValidatorPartitionAware(sts *appsv1.StatefulSet, partition string, zones int, pdbConfig *config, log *spanlogger.SpanLogger) *validatorPartitionAware {
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
		sts:       sts,
		partition: partition,
		zones:     zones,
		pdbConfig: pdbConfig,
		log:       log,
		result:    &zoneStatusResult{},
		matcher:   partitionMatcher,
	}
}

func (v *validatorPartitionAware) considerSts(otherSts *appsv1.StatefulSet) bool {
	return otherSts.UID != v.sts.UID
}

func (v *validatorPartitionAware) accumulateResult(_ *appsv1.StatefulSet, r *zoneStatusResult) error {
	v.result.tested += r.tested
	v.result.notReady += r.notReady
	v.result.unknown += r.unknown
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
