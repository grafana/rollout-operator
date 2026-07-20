package admission

import (
	"context"
	"encoding/json"
	"fmt"

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

// PhasedDeployment is a mutating webhook that pauses opted-in dependent Deployments
// when their shared rollout revision changes, and re-applies pause while the gate is active.
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

	dependsOn := phased.DependsOn(dep)
	if dependsOn == "" {
		// Upstream / first Deployment in a chain: clear leftover gate state if present.
		if patch := clearGateStatePatch(dep); patch != nil {
			level.Info(logger).Log("msg", "clearing leftover phased gate state from non-dependent deployment")
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
		level.Debug(logger).Log("msg", "phased gate complete for revision, allowing", "revision", revision)
		return &v1.AdmissionResponse{Allowed: true}
	}

	var oldDep *appsv1.Deployment
	if len(ar.Request.OldObject.Raw) > 0 {
		oldObj, _, err := codecs.UniversalDeserializer().Decode(ar.Request.OldObject.Raw, nil, nil)
		if err == nil {
			oldDep, _ = oldObj.(*appsv1.Deployment)
		}
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
		return &v1.AdmissionResponse{Allowed: true}
	}

	level.Info(logger).Log(
		"msg", "pausing deployment for phased rollout gate",
		"depends_on", dependsOn,
		"revision", revision,
		"phase", phased.Phase(dep),
	)
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

	if needsNewGate {
		setAnn(config.RolloutDependencyPhaseAnnotationKey, config.RolloutDependencyPhaseWaiting)
		setAnn(config.RolloutDependencyRevisionAnnotationKey, revision)
		setAnn(config.RolloutDependencyReasonAnnotationKey, "waiting for upstream deployment")
		setAnn(config.RolloutHadPausedAnnotationKey, hadPaused)
		if dep.Annotations != nil && dep.Annotations[config.RolloutSoakStartedAtAnnotationKey] != "" {
			ops = append(ops, jsonPatchOp{Op: "remove", Path: phased.AnnotationJSONPointer(config.RolloutSoakStartedAtAnnotationKey)})
		}
		if dep.Annotations != nil && dep.Annotations[config.RolloutRestartBaselineAnnotationKey] != "" {
			ops = append(ops, jsonPatchOp{Op: "remove", Path: phased.AnnotationJSONPointer(config.RolloutRestartBaselineAnnotationKey)})
		}
	} else {
		setAnn(config.RolloutHadPausedAnnotationKey, hadPaused)
		if phased.Phase(dep) == "" {
			setAnn(config.RolloutDependencyPhaseAnnotationKey, config.RolloutDependencyPhaseWaiting)
			setAnn(config.RolloutDependencyRevisionAnnotationKey, revision)
			setAnn(config.RolloutDependencyReasonAnnotationKey, "waiting for upstream deployment")
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
		config.RolloutSoakStartedAtAnnotationKey,
		config.RolloutRestartBaselineAnnotationKey,
		config.RolloutHadPausedAnnotationKey,
		config.RolloutResumeAnnotationKey,
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
