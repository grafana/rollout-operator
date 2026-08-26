package admission

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/go-kit/log"
	"github.com/go-kit/log/level"
	"github.com/grafana/dskit/spanlogger"
	v1 "k8s.io/api/admission/v1"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/grafana/rollout-operator/pkg/config"
	"github.com/grafana/rollout-operator/pkg/phased"
)

const (
	PhasedDeploymentWebhookPath = "/admission/phased-deployment"
)

type jsonPatchOp struct {
	Op    string      `json:"op"`
	Path  string      `json:"path"`
	Value interface{} `json:"value,omitempty"`
}

// PhasedDeployment is a mutating webhook that pauses opted-in Deployments with canary
// dependencies when their shared rollout revision changes, and re-applies pause while
// the gate is active. A time-limited bypass annotation skips gating.
func PhasedDeployment(ctx context.Context, l log.Logger, ar v1.AdmissionReview, _ *kubernetes.Clientset) *v1.AdmissionResponse {
	logger, ctx := spanlogger.New(ctx, l, "admission.PhasedDeployment()", tenantResolver)
	defer logger.Finish()
	_ = ctx

	if ar.Request == nil {
		return &v1.AdmissionResponse{Allowed: true}
	}

	logger.SetSpanAndLogTag("object.name", ar.Request.Name)
	logger.SetSpanAndLogTag("object.namespace", ar.Request.Namespace)
	logger.SetSpanAndLogTag("object.operation", string(ar.Request.Operation))

	if ar.Request.Kind.Kind != "Deployment" {
		return allowWarn(logger, fmt.Sprintf("unsupported kind %s, allowing", ar.Request.Kind.Kind))
	}

	obj, _, err := codecs.UniversalDeserializer().Decode(ar.Request.Object.Raw, nil, nil)
	if err != nil {
		return allowErr(logger, "can't decode object, allowing the change", err)
	}
	dep, ok := obj.(*appsv1.Deployment)
	if !ok {
		return allowWarn(logger, fmt.Sprintf("unexpected type %T, allowing", obj))
	}

	if !phased.IsOptedIn(dep) {
		level.Debug(logger).Log("msg", "deployment not opted into phased rollouts, allowing")
		return &v1.AdmissionResponse{Allowed: true}
	}

	canaries := phased.Canaries(dep)
	if len(canaries) == 0 {
		// Canary / first Deployment in a chain: clear leftover gate state if present.
		if patch := clearGateStatePatch(dep); patch != nil {
			level.Info(logger).Log("msg", "clearing leftover phased gate state from canary deployment")
			return mutate(patch)
		}
		return &v1.AdmissionResponse{Allowed: true}
	}

	if until, ok, err := phased.BypassUntil(dep); err != nil {
		level.Warn(logger).Log("msg", "invalid rollout-bypass-until, ignoring", "err", err)
	} else if ok && phased.BypassActive(dep, time.Now()) {
		level.Info(logger).Log(
			"msg", "phased rollout bypass active, allowing without gate",
			"revision", phased.Revision(dep),
			"bypass_until", until.Format(time.RFC3339),
			"gate_phase", phased.Phase(dep),
			"paused", dep.Spec.Paused,
		)
		if patch := bypassReleasePatch(dep); patch != nil {
			return mutate(patch)
		}
		return &v1.AdmissionResponse{Allowed: true}
	}

	revision := phased.Revision(dep)
	if revision == "" {
		level.Warn(logger).Log("msg", "phased deployment missing rollout revision, allowing without gate")
		return &v1.AdmissionResponse{Allowed: true}
	}

	// Gate already completed for this revision: allow (including unpause).
	if phased.Phase(dep) == config.RolloutDependencyPhaseComplete && phased.DependencyRevision(dep) == revision {
		level.Debug(logger).Log(
			"msg", "phased gate complete for revision, allowing",
			"revision", revision,
			"paused", dep.Spec.Paused,
		)
		return &v1.AdmissionResponse{Allowed: true}
	}

	var oldDep *appsv1.Deployment
	if len(ar.Request.OldObject.Raw) > 0 {
		oldObj, _, err := codecs.UniversalDeserializer().Decode(ar.Request.OldObject.Raw, nil, nil)
		if err == nil {
			oldDep, _ = oldObj.(*appsv1.Deployment)
		}
	}

	needsNewGate := phased.NeedsNewGate(dep)
	if needsNewGate {
		previousRevision := phased.DependencyRevision(dep)
		if oldDep != nil && previousRevision == "" {
			previousRevision = phased.Revision(oldDep)
		}
		level.Info(logger).Log(
			"msg", "phased deployment revision change detected",
			"previous_revision", previousRevision,
			"target_revision", revision,
			"canaries", strings.Join(canaries, ","),
		)
	}

	patch, err := buildGatePatch(dep, oldDep, revision)
	if err != nil {
		level.Error(logger).Log("msg", "failed to build phased gate patch", "err", err)
		return &v1.AdmissionResponse{
			Allowed: false,
			Result:  &metav1.Status{Message: err.Error()},
		}
	}
	if patch == nil {
		level.Debug(logger).Log(
			"msg", "phased deployment gate already enforced",
			"revision", revision,
			"phase", phased.Phase(dep),
			"paused", dep.Spec.Paused,
		)
		return &v1.AdmissionResponse{Allowed: true}
	}

	if !needsNewGate && !dep.Spec.Paused {
		level.Warn(logger).Log(
			"msg", "re-pausing deployment while phased rollout gate is active",
			"canaries", strings.Join(canaries, ","),
			"revision", revision,
			"phase", phased.Phase(dep),
		)
	} else {
		level.Info(logger).Log(
			"msg", "pausing deployment for phased rollout gate",
			"canaries", strings.Join(canaries, ","),
			"revision", revision,
			"phase", phased.Phase(dep),
			"had_paused", resolveHadPaused(dep, oldDep, needsNewGate),
		)
	}
	return mutate(patch)
}

func mutate(patch []byte) *v1.AdmissionResponse {
	pt := v1.PatchTypeJSONPatch
	return &v1.AdmissionResponse{
		Allowed:   true,
		Patch:     patch,
		PatchType: &pt,
	}
}

func buildGatePatch(dep, oldDep *appsv1.Deployment, revision string) ([]byte, error) {
	ops := []jsonPatchOp{}

	if dep.Annotations == nil {
		ops = append(ops, jsonPatchOp{Op: "add", Path: "/metadata/annotations", Value: map[string]string{}})
	}

	needsNewGate := phased.NeedsNewGate(dep)
	hadPaused := resolveHadPaused(dep, oldDep, needsNewGate)

	if !dep.Spec.Paused {
		ops = append(ops, jsonPatchOp{Op: "add", Path: "/spec/paused", Value: true})
	}

	setAnn := func(key, value string) {
		cur := ""
		if dep.Annotations != nil {
			cur = dep.Annotations[key]
		}
		if cur == value {
			return
		}
		ops = append(ops, jsonPatchOp{Op: "add", Path: phased.AnnotationJSONPointer(key), Value: value})
	}
	removeAnn := func(key string) {
		if dep.Annotations != nil && dep.Annotations[key] != "" {
			ops = append(ops, jsonPatchOp{Op: "remove", Path: phased.AnnotationJSONPointer(key)})
		}
	}

	if needsNewGate {
		setAnn(config.RolloutDependencyPhaseAnnotationKey, config.RolloutDependencyPhaseWaiting)
		setAnn(config.RolloutDependencyRevisionAnnotationKey, revision)
		setAnn(config.RolloutDependencyReasonAnnotationKey, "waiting for canary deployment(s)")
		setAnn(config.RolloutHadPausedAnnotationKey, hadPaused)
		removeAnn(config.RolloutCanariesReadyRevisionAnnotationKey)
	} else {
		setAnn(config.RolloutHadPausedAnnotationKey, hadPaused)
		if phased.Phase(dep) == "" {
			setAnn(config.RolloutDependencyPhaseAnnotationKey, config.RolloutDependencyPhaseWaiting)
			setAnn(config.RolloutDependencyRevisionAnnotationKey, revision)
			setAnn(config.RolloutDependencyReasonAnnotationKey, "waiting for canary deployment(s)")
		}
	}

	if len(ops) == 0 {
		return nil, nil
	}
	return json.Marshal(ops)
}

func resolveHadPaused(dep, oldDep *appsv1.Deployment, needsNewGate bool) string {
	if !needsNewGate {
		if dep.Annotations != nil && dep.Annotations[config.RolloutHadPausedAnnotationKey] != "" {
			return dep.Annotations[config.RolloutHadPausedAnnotationKey]
		}
		if oldDep != nil && oldDep.Annotations != nil && oldDep.Annotations[config.RolloutHadPausedAnnotationKey] != "" {
			return oldDep.Annotations[config.RolloutHadPausedAnnotationKey]
		}
		return phased.HadPausedAnnotationFalse
	}

	// New gate: preserve prior user pause intent across revision changes.
	if oldDep != nil {
		if wasPausedByOurGate(oldDep) && oldDep.Annotations != nil && oldDep.Annotations[config.RolloutHadPausedAnnotationKey] != "" {
			return oldDep.Annotations[config.RolloutHadPausedAnnotationKey]
		}
		if oldDep.Spec.Paused && !wasPausedByOurGate(oldDep) {
			return phased.HadPausedAnnotationTrue
		}
		return phased.HadPausedAnnotationFalse
	}
	if dep.Spec.Paused {
		return phased.HadPausedAnnotationTrue
	}
	return phased.HadPausedAnnotationFalse
}

func wasPausedByOurGate(d *appsv1.Deployment) bool {
	if d == nil || !d.Spec.Paused {
		return false
	}
	return phased.GateActive(d)
}

func clearGateStatePatch(dep *appsv1.Deployment) []byte {
	if dep.Annotations == nil {
		return nil
	}
	keys := []string{
		config.RolloutDependencyPhaseAnnotationKey,
		config.RolloutDependencyRevisionAnnotationKey,
		config.RolloutDependencyReasonAnnotationKey,
		config.RolloutHadPausedAnnotationKey,
		config.RolloutCanariesReadyRevisionAnnotationKey,
	}
	hasGateState := false
	for _, key := range keys {
		if dep.Annotations[key] != "" {
			hasGateState = true
			break
		}
	}
	if !hasGateState {
		return nil
	}

	ops := []jsonPatchOp{}
	hadPaused := dep.Annotations[config.RolloutHadPausedAnnotationKey] == phased.HadPausedAnnotationTrue
	phase := dep.Annotations[config.RolloutDependencyPhaseAnnotationKey]
	gateWasActive := phase != "" && phase != config.RolloutDependencyPhaseComplete
	for _, key := range keys {
		if dep.Annotations[key] != "" {
			ops = append(ops, jsonPatchOp{Op: "remove", Path: phased.AnnotationJSONPointer(key)})
		}
	}
	// Only unpause when releasing an active gate that we owned.
	if gateWasActive && dep.Spec.Paused && !hadPaused {
		ops = append(ops, jsonPatchOp{Op: "add", Path: "/spec/paused", Value: false})
	}
	if len(ops) == 0 {
		return nil
	}
	b, err := json.Marshal(ops)
	if err != nil {
		return nil
	}
	return b
}

// bypassReleasePatch unpauses a Deployment that is still held by an active gate when bypass is used.
func bypassReleasePatch(dep *appsv1.Deployment) []byte {
	if !phased.GateActive(dep) && !dep.Spec.Paused {
		return nil
	}
	hadPaused := dep.Annotations != nil && dep.Annotations[config.RolloutHadPausedAnnotationKey] == phased.HadPausedAnnotationTrue
	ops := []jsonPatchOp{}
	if dep.Annotations == nil {
		ops = append(ops, jsonPatchOp{Op: "add", Path: "/metadata/annotations", Value: map[string]string{}})
	}
	revision := phased.Revision(dep)
	setAnn := func(key, value string) {
		cur := ""
		if dep.Annotations != nil {
			cur = dep.Annotations[key]
		}
		if cur == value {
			return
		}
		ops = append(ops, jsonPatchOp{Op: "add", Path: phased.AnnotationJSONPointer(key), Value: value})
	}
	if revision != "" {
		setAnn(config.RolloutDependencyPhaseAnnotationKey, config.RolloutDependencyPhaseComplete)
		setAnn(config.RolloutDependencyRevisionAnnotationKey, revision)
		setAnn(config.RolloutDependencyReasonAnnotationKey, "bypassed until "+strings.TrimSpace(dep.Annotations[config.RolloutBypassUntilAnnotationKey]))
	}
	if dep.Spec.Paused && !hadPaused {
		ops = append(ops, jsonPatchOp{Op: "add", Path: "/spec/paused", Value: false})
	}
	if len(ops) == 0 {
		return nil
	}
	b, err := json.Marshal(ops)
	if err != nil {
		return nil
	}
	return b
}
