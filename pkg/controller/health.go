package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/go-kit/log/level"
	v1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/grafana/rollout-operator/pkg/config"
	"github.com/grafana/rollout-operator/pkg/healthcheck"
)

// HealthGate evaluates cell-level health before progressing to the next zone.
type HealthGate interface {
	Evaluate(ctx context.Context, req healthcheck.Request) healthcheck.Decision
}

// SetHealthCheck wires optional health-check gating into the controller.
func (c *RolloutController) SetHealthCheck(gate HealthGate, metrics *healthcheck.Metrics) {
	c.healthGate = gate
	c.healthMetrics = metrics
}

func (c *RolloutController) maybeEvaluateHealthGate(ctx context.Context, groupName string, sets []*v1.StatefulSet, next *v1.StatefulSet) (pause bool, err error) {
	if c.healthGate == nil {
		return false, nil
	}
	// Binding is per-StatefulSet: only gate when the zone about to roll references a check.
	if strings.TrimSpace(next.Annotations[config.RolloutHealthCheckAnnotationKey]) == "" {
		return false, nil
	}

	needsGate, err := c.shouldGateBeforeStatefulSet(sets, next)
	if err != nil {
		return false, err
	}
	if !needsGate {
		return false, nil
	}

	// Skip PromQL evaluation when the next zone is paused; it will not roll anyway.
	if next.Annotations[config.RolloutPausedAnnotationKey] == config.RolloutPausedAnnotationValue {
		return false, nil
	}

	candidatePods, stablePods, err := c.listCandidateAndStablePods(sets)
	if err != nil {
		return false, err
	}
	baseline := earliestHealthCheckStartedAt(sets)
	if baseline.IsZero() {
		msg := fmt.Sprintf("health-check baseline timestamp missing for group %s; pausing before StatefulSet %s", groupName, next.Name)
		level.Warn(c.logger).Log("msg", msg, "group", groupName, "statefulset", next.Name)
		if c.healthMetrics != nil {
			c.healthMetrics.Blocked.WithLabelValues(groupName).Set(1)
		}
		return true, nil
	}

	decision := c.healthGate.Evaluate(ctx, healthcheck.Request{
		RolloutGroup:  groupName,
		Namespace:     c.namespace,
		Sets:          sets,
		NextSTS:       next,
		CandidatePods: candidatePods,
		StablePods:    stablePods,
		BaselineTime:  baseline,
	})
	return decision.ShouldPause, nil
}

func groupHasHealthCheckAnnotation(sets []*v1.StatefulSet) bool {
	for _, sts := range sets {
		if strings.TrimSpace(sts.Annotations[config.RolloutHealthCheckAnnotationKey]) != "" {
			return true
		}
	}
	return false
}

// shouldGateBeforeStatefulSet is true when next still has pods to update, has not started
// rolling yet, and at least one other StatefulSet in the group already has candidate pods.
func (c *RolloutController) shouldGateBeforeStatefulSet(sets []*v1.StatefulSet, next *v1.StatefulSet) (bool, error) {
	outdated, err := c.podsNotMatchingUpdateRevision(next)
	if err != nil {
		return false, err
	}
	if len(outdated) == 0 {
		return false, nil
	}

	nextCandidates, err := c.podsMatchingUpdateRevision(next)
	if err != nil {
		return false, err
	}
	if len(nextCandidates) > 0 {
		// Zone already mid-roll; do not re-gate.
		return false, nil
	}

	for _, sts := range sets {
		if sts.Name == next.Name {
			continue
		}
		candidates, err := c.podsMatchingUpdateRevision(sts)
		if err != nil {
			return false, err
		}
		if len(candidates) > 0 {
			return true, nil
		}
	}
	return false, nil
}

func (c *RolloutController) podsMatchingUpdateRevision(sts *v1.StatefulSet) ([]*corev1.Pod, error) {
	updateRev := sts.Status.UpdateRevision
	if updateRev == "" {
		return nil, fmt.Errorf("updateRevision is empty for StatefulSet %s", sts.Name)
	}
	pods, err := c.listPodsByStatefulSet(sts)
	if err != nil {
		return nil, err
	}
	var matching []*corev1.Pod
	for _, pod := range pods {
		if pod.Labels[v1.ControllerRevisionHashLabelKey] == updateRev {
			matching = append(matching, pod)
		}
	}
	return matching, nil
}

func (c *RolloutController) listCandidateAndStablePods(sets []*v1.StatefulSet) (candidates, stable []*corev1.Pod, err error) {
	for _, sts := range sets {
		pods, listErr := c.listPodsByStatefulSet(sts)
		if listErr != nil {
			return nil, nil, listErr
		}
		updateRev := sts.Status.UpdateRevision
		for _, pod := range pods {
			if pod.Labels[v1.ControllerRevisionHashLabelKey] == updateRev {
				candidates = append(candidates, pod)
			} else {
				stable = append(stable, pod)
			}
		}
	}
	return candidates, stable, nil
}

func (c *RolloutController) ensureHealthCheckStartedAt(ctx context.Context, sets []*v1.StatefulSet) error {
	now := time.Now().UTC()
	for _, sts := range sets {
		outdated, err := c.podsNotMatchingUpdateRevision(sts)
		if err != nil {
			return err
		}
		if len(outdated) == 0 {
			continue
		}
		updateRev := sts.Status.UpdateRevision
		existing := healthcheck.ParseStartedAtAnnotation(sts.Annotations[config.RolloutHealthCheckStartedAtAnnotationKey], updateRev)
		if !existing.IsZero() {
			continue
		}
		value := healthcheck.FormatStartedAtAnnotation(updateRev, now)
		if err := c.patchStatefulSetAnnotation(ctx, sts, config.RolloutHealthCheckStartedAtAnnotationKey, value); err != nil {
			return fmt.Errorf("failed to patch started-at on %s: %w", sts.Name, err)
		}
		if sts.Annotations == nil {
			sts.Annotations = map[string]string{}
		}
		sts.Annotations[config.RolloutHealthCheckStartedAtAnnotationKey] = value
	}
	return nil
}

func earliestHealthCheckStartedAt(sets []*v1.StatefulSet) time.Time {
	var earliest time.Time
	for _, sts := range sets {
		t := healthcheck.ParseStartedAtAnnotation(sts.Annotations[config.RolloutHealthCheckStartedAtAnnotationKey], sts.Status.UpdateRevision)
		if t.IsZero() {
			continue
		}
		if earliest.IsZero() || t.Before(earliest) {
			earliest = t
		}
	}
	return earliest
}

func (c *RolloutController) patchStatefulSetAnnotation(ctx context.Context, sts *v1.StatefulSet, key, value string) error {
	patch := map[string]interface{}{
		"metadata": map[string]interface{}{
			"annotations": map[string]string{
				key: value,
			},
		},
	}
	data, err := json.Marshal(patch)
	if err != nil {
		return err
	}
	_, err = c.kubeClient.AppsV1().StatefulSets(c.namespace).Patch(ctx, sts.Name, types.MergePatchType, data, metav1.PatchOptions{})
	return err
}
