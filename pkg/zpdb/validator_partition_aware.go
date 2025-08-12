package zpdb

import (
	"errors"
	"fmt"

	"github.com/go-kit/log/level"
	"github.com/grafana/dskit/spanlogger"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
)

type ValidatorPartitionAware struct {
	sts       *appsv1.StatefulSet
	result    *ZoneStatusResult
	partition string
	matcher   *PartitionMatcher
	zones     int
	pdbConfig *Config
	log       *spanlogger.SpanLogger
}

func NewValidatorPartitionAware(sts *appsv1.StatefulSet, partition string, zones int, pdbConfig *Config, log *spanlogger.SpanLogger) *ValidatorPartitionAware {
	return &ValidatorPartitionAware{
		sts:       sts,
		partition: partition,
		zones:     zones,
		pdbConfig: pdbConfig,
		log:       log,
		result:    &ZoneStatusResult{},
		matcher: &PartitionMatcher{
			Same: func(pd *corev1.Pod) bool {
				thisPartition, err := pdbConfig.PodPartition(pd)
				if err != nil {
					// the partition name was successfully extracted from the pod being evicted
					// so if this regex has failed then the assumption is that it is not the same partition, as would have a different naming convention
					// or the regex is too tightly defined
					level.Error(log).Log("msg", "Unable to extract partition from pod name - check the pod partition name regex", "name", pd.Name)
				}
				return thisPartition == partition
			},
		},
	}
}

func (v *ValidatorPartitionAware) ConsiderSts(otherSts *appsv1.StatefulSet) bool {
	return otherSts.UID != v.sts.UID
}

func (v *ValidatorPartitionAware) AccumulateResult(_ *appsv1.StatefulSet, r *ZoneStatusResult) error {
	v.result.Tested += r.Tested
	v.result.NotReady += r.NotReady
	v.result.Unknown += r.Unknown
	return nil
}

func (v *ValidatorPartitionAware) Validate(maxUnavailable int) error {
	if v.result.NotReady+v.result.Unknown >= maxUnavailable {
		return errors.New(pdbMessage(v.result, "partition "+v.partition))
	}
	return nil
}

func (v *ValidatorPartitionAware) SuccessMessage() string {
	return fmt.Sprintf("zpdb met for partition %s across %d zones", v.partition, v.zones)
}

func (v *ValidatorPartitionAware) ConsiderPod() *PartitionMatcher {
	return v.matcher
}
