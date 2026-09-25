package phased

import (
	"fmt"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"

	"github.com/grafana/rollout-operator/pkg/config"
)

const (
	HadPausedAnnotationTrue  = "true"
	HadPausedAnnotationFalse = "false"
)

// IsOptedIn reports whether the Deployment participates in phased rollouts.
func IsOptedIn(d *appsv1.Deployment) bool {
	if d == nil || d.Labels == nil {
		return false
	}
	return d.Labels[config.RolloutPhasedLabelKey] == config.RolloutPhasedLabelValue
}

// Canaries returns the canary Deployment names this Deployment depends on.
// Values are comma-separated in grafana.com/rollout-canary.
func Canaries(d *appsv1.Deployment) []string {
	if d == nil || d.Annotations == nil {
		return nil
	}
	raw := strings.TrimSpace(d.Annotations[config.RolloutCanaryAnnotationKey])
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	seen := map[string]struct{}{}
	for _, p := range parts {
		name := strings.TrimSpace(p)
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	return out
}

// Revision returns the shared rollout revision stamp.
func Revision(d *appsv1.Deployment) string {
	if d == nil || d.Annotations == nil {
		return ""
	}
	return strings.TrimSpace(d.Annotations[config.RolloutRevisionAnnotationKey])
}

// Phase returns the current dependency gate phase.
func Phase(d *appsv1.Deployment) string {
	if d == nil || d.Annotations == nil {
		return ""
	}
	return d.Annotations[config.RolloutDependencyPhaseAnnotationKey]
}

// DependencyRevision returns the revision currently being gated.
func DependencyRevision(d *appsv1.Deployment) string {
	if d == nil || d.Annotations == nil {
		return ""
	}
	return d.Annotations[config.RolloutDependencyRevisionAnnotationKey]
}

// CanariesReadyRevision returns the revision for which every canary reached full readiness.
func CanariesReadyRevision(d *appsv1.Deployment) string {
	if d == nil || d.Annotations == nil {
		return ""
	}
	return strings.TrimSpace(d.Annotations[config.RolloutCanariesReadyRevisionAnnotationKey])
}

// BypassUntil parses grafana.com/rollout-bypass-until. ok is false when unset.
func BypassUntil(d *appsv1.Deployment) (until time.Time, ok bool, err error) {
	if d == nil || d.Annotations == nil {
		return time.Time{}, false, nil
	}
	raw := strings.TrimSpace(d.Annotations[config.RolloutBypassUntilAnnotationKey])
	if raw == "" {
		return time.Time{}, false, nil
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}, false, fmt.Errorf("invalid %s %q: %w", config.RolloutBypassUntilAnnotationKey, raw, err)
	}
	return t, true, nil
}

// BypassActive reports whether a valid bypass-until annotation is still in the future.
func BypassActive(d *appsv1.Deployment, now time.Time) bool {
	until, ok, err := BypassUntil(d)
	if err != nil || !ok {
		return false
	}
	return now.Before(until)
}

// GateActive reports whether the Deployment must stay paused for the current revision.
func GateActive(d *appsv1.Deployment) bool {
	if !IsOptedIn(d) || len(Canaries(d)) == 0 {
		return false
	}
	rev := Revision(d)
	if rev == "" || DependencyRevision(d) != rev {
		return false
	}
	phase := Phase(d)
	return phase != "" && phase != config.RolloutDependencyPhaseComplete
}

// NeedsNewGate reports whether a revision change requires (re)starting the gate.
func NeedsNewGate(d *appsv1.Deployment) bool {
	if !IsOptedIn(d) || len(Canaries(d)) == 0 {
		return false
	}
	rev := Revision(d)
	if rev == "" {
		return false
	}
	return DependencyRevision(d) != rev
}

// IsFullyRolledOut reports whether every replica is updated, ready, and available.
func IsFullyRolledOut(d *appsv1.Deployment) bool {
	if d == nil {
		return false
	}
	if d.Spec.Paused {
		return false
	}
	desired := int32(1)
	if d.Spec.Replicas != nil {
		desired = *d.Spec.Replicas
	}
	if desired == 0 {
		return d.Status.Replicas == 0 && d.Status.ObservedGeneration >= d.Generation
	}
	if d.Status.ObservedGeneration < d.Generation {
		return false
	}
	return d.Status.UpdatedReplicas == desired &&
		d.Status.ReadyReplicas == desired &&
		d.Status.AvailableReplicas == desired &&
		d.Status.Replicas == desired
}

// DetectDependencyCycle walks canary links starting from start.
// deployments is keyed by name. Returns true if a cycle involving start is found.
func DetectDependencyCycle(start string, deployments map[string]*appsv1.Deployment) bool {
	onPath := map[string]struct{}{}
	seen := map[string]struct{}{}
	var walk func(string) bool
	walk = func(cur string) bool {
		if _, ok := onPath[cur]; ok {
			return true
		}
		if _, ok := seen[cur]; ok {
			return false
		}
		onPath[cur] = struct{}{}
		seen[cur] = struct{}{}
		if d, ok := deployments[cur]; ok {
			for _, next := range Canaries(d) {
				if walk(next) {
					return true
				}
			}
		}
		delete(onPath, cur)
		return false
	}
	return walk(start)
}

// AnnotationJSONPointer escapes an annotation key for use in a JSON Patch path.
func AnnotationJSONPointer(key string) string {
	// JSON Pointer: ~ -> ~0, / -> ~1
	escaped := strings.ReplaceAll(key, "~", "~0")
	escaped = strings.ReplaceAll(escaped, "/", "~1")
	return "/metadata/annotations/" + escaped
}
