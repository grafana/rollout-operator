package phased

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/grafana/rollout-operator/pkg/config"
)

func TestCanaries(t *testing.T) {
	d := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
		config.RolloutCanaryAnnotationKey: " zone-a , query-frontend-zone-a,zone-a ",
	}}}
	require.Equal(t, []string{"zone-a", "query-frontend-zone-a"}, Canaries(d))
	require.Nil(t, Canaries(&appsv1.Deployment{}))
}

func TestIsFullyRolledOut(t *testing.T) {
	replicas := int32(3)
	d := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Generation: 2},
		Spec:       appsv1.DeploymentSpec{Replicas: &replicas},
		Status: appsv1.DeploymentStatus{
			ObservedGeneration: 2,
			Replicas:           3,
			UpdatedReplicas:    3,
			ReadyReplicas:      3,
			AvailableReplicas:  3,
		},
	}
	require.True(t, IsFullyRolledOut(d))

	d.Spec.Paused = true
	require.False(t, IsFullyRolledOut(d))
	d.Spec.Paused = false

	d.Status.ReadyReplicas = 2
	require.False(t, IsFullyRolledOut(d))
}

func TestDetectDependencyCycle(t *testing.T) {
	a := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "a", Annotations: map[string]string{config.RolloutCanaryAnnotationKey: "c"}}}
	b := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "b", Annotations: map[string]string{config.RolloutCanaryAnnotationKey: "a"}}}
	c := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "c", Annotations: map[string]string{config.RolloutCanaryAnnotationKey: "b"}}}
	byName := map[string]*appsv1.Deployment{"a": a, "b": b, "c": c}
	require.True(t, DetectDependencyCycle("a", byName))

	c.Annotations = nil
	require.False(t, DetectDependencyCycle("a", byName))

	// Multi-canary diamond is not a cycle.
	main := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "main", Annotations: map[string]string{config.RolloutCanaryAnnotationKey: "left,right"}}}
	left := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "left"}}
	right := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "right"}}
	require.False(t, DetectDependencyCycle("main", map[string]*appsv1.Deployment{"main": main, "left": left, "right": right}))
}

func TestGateActive(t *testing.T) {
	d := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Labels: map[string]string{config.RolloutPhasedLabelKey: config.RolloutPhasedLabelValue},
			Annotations: map[string]string{
				config.RolloutCanaryAnnotationKey:             "upstream",
				config.RolloutRevisionAnnotationKey:           "r1",
				config.RolloutDependencyPhaseAnnotationKey:    config.RolloutDependencyPhaseWaiting,
				config.RolloutDependencyRevisionAnnotationKey: "r1",
			},
		},
	}
	require.True(t, GateActive(d))

	d.Annotations[config.RolloutDependencyPhaseAnnotationKey] = config.RolloutDependencyPhaseComplete
	require.False(t, GateActive(d))
}

func TestBypassActive(t *testing.T) {
	now := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	d := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
		config.RolloutBypassUntilAnnotationKey: now.Add(time.Hour).Format(time.RFC3339),
	}}}
	require.True(t, BypassActive(d, now))
	require.False(t, BypassActive(d, now.Add(2*time.Hour)))

	d.Annotations[config.RolloutBypassUntilAnnotationKey] = "not-a-time"
	require.False(t, BypassActive(d, now))
}
