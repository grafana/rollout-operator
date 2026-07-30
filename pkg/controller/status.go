package controller

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"time"

	v1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/grafana/rollout-operator/pkg/config"
	"github.com/grafana/rollout-operator/pkg/status"
	"github.com/grafana/rollout-operator/pkg/util"
)

// Snapshot builds a read-only rollout status view from informer caches.
func (c *RolloutController) Snapshot(ctx context.Context) (*status.Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	sets, err := c.statefulSetLister.StatefulSets(c.namespace).List(labels.Everything())
	if err != nil {
		return nil, fmt.Errorf("failed to list StatefulSets: %w", err)
	}

	groups := util.GroupStatefulSetsByLabel(sets, config.RolloutGroupLabelKey)
	groupNames := make([]string, 0, len(groups))
	for name := range groups {
		groupNames = append(groupNames, name)
	}
	sort.Strings(groupNames)

	out := &status.Snapshot{
		Namespace:      c.namespace,
		ObservedAt:     time.Now().UTC(),
		Groups:         make([]status.Group, 0, len(groupNames)),
		Configurations: make(map[string][]status.ConfigurationEntry, len(sets)),
	}

	for _, groupName := range groupNames {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		groupSets := groups[groupName]
		util.SortStatefulSets(groupSets)

		members := make([]status.Member, 0, len(groupSets))
		notReadyMembers := 0
		for _, sts := range groupSets {
			member, err := c.memberStatus(sts)
			if err != nil {
				return nil, err
			}
			out.Configurations[sts.Name] = statefulSetConfiguration(sts)
			if member.NotReady {
				notReadyMembers++
			}
			members = append(members, member)
		}
		applyZoneGating(members)

		group := status.Group{
			Name:    groupName,
			Members: members,
		}
		group.Phase, group.Reason = aggregateGroupPhase(members, notReadyMembers)
		out.Groups = append(out.Groups, group)
	}

	return out, nil
}

var rolloutLabelKeys = []string{
	config.RolloutGroupLabelKey,
	config.NoDownscaleLabelKey,
	config.PrepareDownscaleLabelKey,
	config.MinTimeBetweenZonesDownscaleLabelKey,
}

var rolloutAnnotationKeys = []string{
	config.RolloutMaxUnavailableAnnotationKey,
	config.RolloutDownscaleLeaderAnnotationKey,
	config.RolloutLeaderReadyAnnotationKey,
	config.RolloutMirrorReplicasFromResourceNameAnnotationKey,
	config.RolloutMirrorReplicasFromResourceKindAnnotationKey,
	config.RolloutMirrorReplicasFromResourceAPIVersionAnnotationKey,
	config.RolloutMirrorReplicasFromResourceWriteBackStatusReplicas,
	config.RolloutDelayedDownscaleAnnotationKey,
	config.RolloutDelayedDownscalePrepareUrlAnnotationKey,
	config.RolloutForceReplicasAnnotationKey,
	config.RolloutPausedAnnotationKey,
	config.MinTimeBetweenZonesDownscaleAnnotationKey,
	config.PrepareDownscalePathAnnotationKey,
	config.PrepareDownscalePortAnnotationKey,
	config.LastDownscaleAnnotationKey,
}

func statefulSetConfiguration(sts *v1.StatefulSet) []status.ConfigurationEntry {
	replicas := int32(0)
	if sts.Spec.Replicas != nil {
		replicas = *sts.Spec.Replicas
	}
	entries := []status.ConfigurationEntry{
		{Source: "spec", Name: "replicas", Value: strconv.FormatInt(int64(replicas), 10)},
		{Source: "spec", Name: "updateStrategy.type", Value: string(sts.Spec.UpdateStrategy.Type)},
	}

	for _, key := range rolloutLabelKeys {
		if value, ok := sts.Labels[key]; ok {
			entries = append(entries, status.ConfigurationEntry{Source: "label", Name: key, Value: value})
		}
	}
	for _, key := range rolloutAnnotationKeys {
		if value, ok := sts.Annotations[key]; ok {
			entries = append(entries, status.ConfigurationEntry{Source: "annotation", Name: key, Value: value})
		}
	}
	return entries
}

func (c *RolloutController) memberStatus(sts *v1.StatefulSet) (status.Member, error) {
	desired := int32(0)
	if sts.Spec.Replicas != nil {
		desired = *sts.Spec.Replicas
	}

	paused := sts.Annotations[config.RolloutPausedAnnotationKey] == config.RolloutPausedAnnotationValue
	member := status.Member{
		Name:            sts.Name,
		DesiredReplicas: desired,
		ReadyReplicas:   sts.Status.ReadyReplicas,
		CurrentRevision: sts.Status.CurrentRevision,
		UpdateRevision:  sts.Status.UpdateRevision,
		Paused:          paused,
		UpdateStrategy:  string(sts.Spec.UpdateStrategy.Type),
	}

	pods, err := c.listPodsByStatefulSet(sts)
	if err != nil {
		return status.Member{}, err
	}
	member.TotalPods = len(pods)

	updateRev := sts.Status.UpdateRevision
	updated := 0
	for _, pod := range pods {
		if pod.Labels[v1.ControllerRevisionHashLabelKey] == updateRev {
			updated++
		}
	}
	member.UpdatedPods = updated

	if sts.Spec.UpdateStrategy.Type != v1.OnDeleteStatefulSetStrategyType {
		member.Phase = status.PhaseDegraded
		member.Reason = fmt.Sprintf("update strategy is %s; OnDelete is required", sts.Spec.UpdateStrategy.Type)
		return member, nil
	}

	hasNotReady := statefulSetHasNotReadyPods(sts, pods)
	member.NotReady = hasNotReady

	// Only pods present but not yet on updateRevision count as an in-progress rollout.
	// Missing pods during scale-up are readiness/creation lag (hasNotReady), not a revision rollout.
	needsUpdate := updateRev != "" && updated < len(pods)

	switch {
	case needsUpdate && paused:
		member.Phase = status.PhasePaused
		member.Reason = "rollout paused"
	case needsUpdate:
		member.Phase = status.PhaseProgressing
		member.Reason = fmt.Sprintf("%d of %d pods updated", updated, len(pods))
	case hasNotReady:
		member.Phase = status.PhaseWaiting
		member.Reason = "waiting for pods to become Ready"
	case sts.Status.CurrentRevision != "" && updateRev != "" && sts.Status.CurrentRevision != updateRev:
		// Pods match the update revision and are ready; currentRevision lag is transient.
		member.Phase = status.PhaseComplete
		member.Reason = "pods updated; current revision pending"
	default:
		member.Phase = status.PhaseComplete
		member.Reason = ""
	}

	return member, nil
}

// statefulSetHasNotReadyPods mirrors hasStatefulSetNotReadyPods without emitting Info logs.
func statefulSetHasNotReadyPods(sts *v1.StatefulSet, pods []*corev1.Pod) bool {
	if sts.Status.Replicas != sts.Status.ReadyReplicas {
		return true
	}
	if len(pods) < int(sts.Status.Replicas) {
		return true
	}
	return len(notRunningAndReady(pods)) > 0
}

const multipleNotReadyReason = "multiple StatefulSets have not-Ready pods"

// applyZoneGating mirrors reconcile ordering: only one StatefulSet is actively
// updated at a time. Multi not-ready (including paused) blocks everything; a sole
// not-ready paused set does not, because updateStatefulSetPods skips it.
func applyZoneGating(members []status.Member) {
	var notReady, notReadyActive []string
	for _, m := range members {
		if !m.NotReady {
			continue
		}
		notReady = append(notReady, m.Name)
		if !m.Paused {
			notReadyActive = append(notReadyActive, m.Name)
		}
	}
	if len(notReady) > 1 {
		for i := range members {
			if members[i].Phase == status.PhaseProgressing {
				members[i].Phase = status.PhaseWaiting
				members[i].Reason = multipleNotReadyReason
			}
		}
		return
	}
	if len(notReadyActive) == 1 {
		blocker := notReadyActive[0]
		for i := range members {
			m := &members[i]
			if m.Name == blocker {
				continue
			}
			if m.Phase == status.PhaseProgressing {
				m.Phase = status.PhaseWaiting
				m.Reason = fmt.Sprintf("waiting for %s", blocker)
			}
		}
		return
	}

	var blocker string
	for i := range members {
		m := &members[i]
		switch m.Phase {
		case status.PhaseComplete, status.PhaseDegraded, status.PhasePaused:
			continue
		case status.PhaseProgressing, status.PhaseWaiting:
			if blocker == "" {
				blocker = m.Name
				continue
			}
			if m.Phase == status.PhaseProgressing {
				m.Phase = status.PhaseWaiting
				m.Reason = fmt.Sprintf("waiting for %s", blocker)
			}
		}
	}
}

func aggregateGroupPhase(members []status.Member, notReadyMembers int) (status.Phase, string) {
	if len(members) == 0 {
		return status.PhaseUnknown, "no StatefulSets"
	}

	for _, m := range members {
		if m.Phase == status.PhaseDegraded {
			return status.PhaseDegraded, m.Reason
		}
	}

	if notReadyMembers > 1 {
		return status.PhaseWaiting, multipleNotReadyReason
	}

	priority := []status.Phase{
		status.PhaseProgressing,
		status.PhaseWaiting,
		status.PhasePaused,
		status.PhaseComplete,
	}
	for _, phase := range priority {
		for _, m := range members {
			if m.Phase == phase {
				return phase, m.Reason
			}
		}
	}
	return status.PhaseUnknown, ""
}
