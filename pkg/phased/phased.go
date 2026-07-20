package phased

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/prometheus/common/model"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/grafana/rollout-operator/pkg/config"
)

const (
	DefaultSoakDuration      = 5 * time.Minute
	DefaultRestartThreshold  = 0.10
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

// DependsOn returns the upstream Deployment name, or empty if none.
func DependsOn(d *appsv1.Deployment) string {
	if d == nil || d.Annotations == nil {
		return ""
	}
	return strings.TrimSpace(d.Annotations[config.RolloutDependsOnAnnotationKey])
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

// ResumeRevision returns the revision requested for an explicit resume bypass.
func ResumeRevision(d *appsv1.Deployment) string {
	if d == nil || d.Annotations == nil {
		return ""
	}
	return strings.TrimSpace(d.Annotations[config.RolloutResumeAnnotationKey])
}

// GateActive reports whether the Deployment must stay paused for the current revision.
func GateActive(d *appsv1.Deployment) bool {
	if !IsOptedIn(d) || DependsOn(d) == "" {
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
	if !IsOptedIn(d) || DependsOn(d) == "" {
		return false
	}
	rev := Revision(d)
	if rev == "" {
		return false
	}
	return DependencyRevision(d) != rev
}

// ParseSoakDuration returns the soak duration from annotations, or the default.
func ParseSoakDuration(annotations map[string]string) (time.Duration, error) {
	if annotations == nil {
		return DefaultSoakDuration, nil
	}
	raw := strings.TrimSpace(annotations[config.RolloutSoakDurationAnnotationKey])
	if raw == "" {
		return DefaultSoakDuration, nil
	}
	d, err := model.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("invalid %s %q: %w", config.RolloutSoakDurationAnnotationKey, raw, err)
	}
	if d <= 0 {
		return 0, fmt.Errorf("%s must be positive, got %q", config.RolloutSoakDurationAnnotationKey, raw)
	}
	return time.Duration(d), nil
}

// ParseRestartThreshold returns the restart ratio threshold from annotations, or the default.
func ParseRestartThreshold(annotations map[string]string) (float64, error) {
	if annotations == nil {
		return DefaultRestartThreshold, nil
	}
	raw := strings.TrimSpace(annotations[config.RolloutRestartThresholdAnnotationKey])
	if raw == "" {
		return DefaultRestartThreshold, nil
	}
	percent := false
	if strings.HasSuffix(raw, "%") {
		percent = true
		raw = strings.TrimSuffix(raw, "%")
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid %s %q: %w", config.RolloutRestartThresholdAnnotationKey, annotations[config.RolloutRestartThresholdAnnotationKey], err)
	}
	if percent {
		v = v / 100
	}
	if v < 0 {
		return 0, fmt.Errorf("%s must be non-negative, got %q", config.RolloutRestartThresholdAnnotationKey, annotations[config.RolloutRestartThresholdAnnotationKey])
	}
	return v, nil
}

// ExceedsRestartThreshold reports whether the measured ratio should block progression.
// A threshold of 0 means any positive restart ratio blocks; otherwise ratio >= threshold blocks.
func ExceedsRestartThreshold(ratio, threshold float64) bool {
	if threshold == 0 {
		return ratio > 0
	}
	return ratio >= threshold
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

// RestartBaseline maps "podUID/containerName" to restart count at soak start.
type RestartBaseline map[string]int32

func baselineKey(podUID types.UID, containerName string) string {
	return string(podUID) + "/" + containerName
}

// BuildRestartBaseline captures current restart counts for the given pods.
func BuildRestartBaseline(pods []*corev1.Pod) RestartBaseline {
	baseline := make(RestartBaseline)
	for _, pod := range pods {
		if pod == nil {
			continue
		}
		for _, cs := range pod.Status.ContainerStatuses {
			baseline[baselineKey(pod.UID, cs.Name)] = cs.RestartCount
		}
	}
	return baseline
}

// EncodeRestartBaseline serializes a baseline for annotation storage.
func EncodeRestartBaseline(baseline RestartBaseline) (string, error) {
	if baseline == nil {
		baseline = RestartBaseline{}
	}
	b, err := json.Marshal(baseline)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// DecodeRestartBaseline parses a baseline annotation value.
func DecodeRestartBaseline(raw string) (RestartBaseline, error) {
	if raw == "" {
		return RestartBaseline{}, nil
	}
	var baseline RestartBaseline
	if err := json.Unmarshal([]byte(raw), &baseline); err != nil {
		return nil, err
	}
	if baseline == nil {
		baseline = RestartBaseline{}
	}
	return baseline, nil
}

// RestartRatio is sum(container restart deltas since baseline) / len(pods).
// Missing baseline entries (new pods/containers) are treated as zero.
func RestartRatio(pods []*corev1.Pod, baseline RestartBaseline) float64 {
	if len(pods) == 0 {
		return 0
	}
	if baseline == nil {
		baseline = RestartBaseline{}
	}
	var totalDelta int64
	for _, pod := range pods {
		if pod == nil {
			continue
		}
		for _, cs := range pod.Status.ContainerStatuses {
			prev := baseline[baselineKey(pod.UID, cs.Name)]
			delta := cs.RestartCount - prev
			if delta > 0 {
				totalDelta += int64(delta)
			}
		}
	}
	return float64(totalDelta) / float64(len(pods))
}

// DetectDependencyCycle walks depends-on links starting from start.
// deployments is keyed by name. Returns true if a cycle involving start is found.
func DetectDependencyCycle(start string, deployments map[string]*appsv1.Deployment) bool {
	seen := map[string]struct{}{}
	cur := start
	for {
		if _, ok := seen[cur]; ok {
			return true
		}
		seen[cur] = struct{}{}
		d, ok := deployments[cur]
		if !ok {
			return false
		}
		next := DependsOn(d)
		if next == "" {
			return false
		}
		cur = next
	}
}

// AnnotationJSONPointer escapes an annotation key for use in a JSON Patch path.
func AnnotationJSONPointer(key string) string {
	// JSON Pointer: ~ -> ~0, / -> ~1
	escaped := strings.ReplaceAll(key, "~", "~0")
	escaped = strings.ReplaceAll(escaped, "/", "~1")
	return "/metadata/annotations/" + escaped
}
