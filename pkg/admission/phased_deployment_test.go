package admission

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/go-kit/log"
	"github.com/stretchr/testify/require"
	admissionv1 "k8s.io/api/admission/v1"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/grafana/rollout-operator/pkg/config"
)

func TestPhasedDeployment_PausesOnRevisionChange(t *testing.T) {
	newDep := phasedDeployment("query-frontend-zone-b", "query-frontend-zone-a", "r2", false, "", "")
	oldDep := phasedDeployment("query-frontend-zone-b", "query-frontend-zone-a", "r1", false, config.RolloutDependencyPhaseComplete, "r1")

	resp := PhasedDeployment(context.Background(), log.NewNopLogger(), admissionReview(oldDep, newDep), nil)
	require.True(t, resp.Allowed)
	require.NotNil(t, resp.PatchType)
	require.NotEmpty(t, resp.Patch)

	var ops []jsonPatchOp
	require.NoError(t, json.Unmarshal(resp.Patch, &ops))
	require.Contains(t, ops, jsonPatchOp{Op: "add", Path: "/spec/paused", Value: true})
}

func TestPhasedDeployment_RePausesWhileGateActive(t *testing.T) {
	newDep := phasedDeployment("b", "a", "r1", false, config.RolloutDependencyPhaseSoaking, "r1")
	oldDep := phasedDeployment("b", "a", "r1", true, config.RolloutDependencyPhaseSoaking, "r1")
	oldDep.Annotations[config.RolloutHadPausedAnnotationKey] = "false"
	newDep.Annotations[config.RolloutHadPausedAnnotationKey] = "false"

	resp := PhasedDeployment(context.Background(), log.NewNopLogger(), admissionReview(oldDep, newDep), nil)
	require.True(t, resp.Allowed)
	require.NotEmpty(t, resp.Patch)

	var ops []jsonPatchOp
	require.NoError(t, json.Unmarshal(resp.Patch, &ops))
	require.Contains(t, ops, jsonPatchOp{Op: "add", Path: "/spec/paused", Value: true})
}

func TestPhasedDeployment_AllowsWhenComplete(t *testing.T) {
	dep := phasedDeployment("b", "a", "r1", false, config.RolloutDependencyPhaseComplete, "r1")
	resp := PhasedDeployment(context.Background(), log.NewNopLogger(), admissionReview(dep, dep), nil)
	require.True(t, resp.Allowed)
	require.Nil(t, resp.Patch)
}

func TestPhasedDeployment_AllowsWithoutDependsOn(t *testing.T) {
	dep := &appsv1.Deployment{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "apps/v1",
			Kind:       "Deployment",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:   "a",
			Labels: map[string]string{config.RolloutPhasedLabelKey: config.RolloutPhasedLabelValue},
			Annotations: map[string]string{
				config.RolloutRevisionAnnotationKey: "r1",
			},
		},
	}
	resp := PhasedDeployment(context.Background(), log.NewNopLogger(), admissionReview(nil, dep), nil)
	require.True(t, resp.Allowed)
	require.Nil(t, resp.Patch)
}

func TestPhasedDeployment_PreservesPreExistingPause(t *testing.T) {
	oldDep := phasedDeployment("b", "a", "r1", true, "", "")
	newDep := phasedDeployment("b", "a", "r2", true, "", "")

	resp := PhasedDeployment(context.Background(), log.NewNopLogger(), admissionReview(oldDep, newDep), nil)
	require.True(t, resp.Allowed)
	require.NotEmpty(t, resp.Patch)

	var ops []jsonPatchOp
	require.NoError(t, json.Unmarshal(resp.Patch, &ops))
	found := false
	for _, op := range ops {
		if op.Path == "/metadata/annotations/grafana.com~1rollout-had-paused" {
			require.Equal(t, "true", op.Value)
			found = true
		}
	}
	require.True(t, found, "expected had-paused annotation in patch")
}

func phasedDeployment(name, dependsOn, revision string, paused bool, phase, depRevision string) *appsv1.Deployment {
	ann := map[string]string{
		config.RolloutDependsOnAnnotationKey: dependsOn,
		config.RolloutRevisionAnnotationKey:  revision,
	}
	if phase != "" {
		ann[config.RolloutDependencyPhaseAnnotationKey] = phase
	}
	if depRevision != "" {
		ann[config.RolloutDependencyRevisionAnnotationKey] = depRevision
	}
	return &appsv1.Deployment{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "apps/v1",
			Kind:       "Deployment",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   "default",
			Labels:      map[string]string{config.RolloutPhasedLabelKey: config.RolloutPhasedLabelValue},
			Annotations: ann,
		},
		Spec: appsv1.DeploymentSpec{Paused: paused},
	}
}

func admissionReview(oldDep, newDep *appsv1.Deployment) admissionv1.AdmissionReview {
	rawNew, err := json.Marshal(newDep)
	if err != nil {
		panic(err)
	}
	ar := admissionv1.AdmissionReview{
		Request: &admissionv1.AdmissionRequest{
			UID:       "test",
			Kind:      metav1.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"},
			Resource:  metav1.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"},
			Name:      newDep.Name,
			Namespace: newDep.Namespace,
			Operation: admissionv1.Update,
			Object:    runtime.RawExtension{Raw: rawNew},
		},
	}
	if oldDep != nil {
		rawOld, err := json.Marshal(oldDep)
		if err != nil {
			panic(err)
		}
		ar.Request.OldObject = runtime.RawExtension{Raw: rawOld}
	}
	return ar
}
