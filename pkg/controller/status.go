package controller

import (
	"context"
	"fmt"
	"sort"
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
		Namespace:  c.namespace,
		ObservedAt: time.Now().UTC(),
		Groups:     make([]status.Group, 0, len(groupNames)),
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
			if memberHasNotReadyPods(member) {
				notReadyMembers++
			}
			members = append(members, member)
		}

		group := status.Group{
			Name:    groupName,
			Members: members,
		}
		group.Phase, group.Reason = aggregateGroupPhase(members, notReadyMembers)
		out.Groups = append(out.Groups, group)
	}

	return out, nil
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
	needsUpdate := updateRev != "" && updated < len(pods)
	// Missing pods also mean the set is not fully updated to the desired revision.
	if updateRev != "" && int32(len(pods)) < desired {
		needsUpdate = true
	}

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

func memberHasNotReadyPods(m status.Member) bool {
	if m.Phase == status.PhaseDegraded {
		return false
	}
	if m.ReadyReplicas < m.DesiredReplicas || m.TotalPods < int(m.DesiredReplicas) {
		return true
	}
	return m.Phase == status.PhaseWaiting
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
		return status.PhaseWaiting, "multiple StatefulSets have not-Ready pods"
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
