package phased

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/grafana/rollout-operator/pkg/config"
)

func TestParseSoakDuration(t *testing.T) {
	d, err := ParseSoakDuration(nil)
	require.NoError(t, err)
	require.Equal(t, DefaultSoakDuration, d)

	d, err = ParseSoakDuration(map[string]string{config.RolloutSoakDurationAnnotationKey: "2m"})
	require.NoError(t, err)
	require.Equal(t, 2*time.Minute, d)

	_, err = ParseSoakDuration(map[string]string{config.RolloutSoakDurationAnnotationKey: "nope"})
	require.Error(t, err)
}

func TestParseRestartThreshold(t *testing.T) {
	v, err := ParseRestartThreshold(nil)
	require.NoError(t, err)
	require.InDelta(t, 0.10, v, 0.0001)

	v, err = ParseRestartThreshold(map[string]string{config.RolloutRestartThresholdAnnotationKey: "10%"})
	require.NoError(t, err)
	require.InDelta(t, 0.10, v, 0.0001)

	v, err = ParseRestartThreshold(map[string]string{config.RolloutRestartThresholdAnnotationKey: "0.25"})
	require.NoError(t, err)
	require.InDelta(t, 0.25, v, 0.0001)
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

func TestRestartRatio(t *testing.T) {
	pods := []*corev1.Pod{
		{
			ObjectMeta: metav1.ObjectMeta{UID: types.UID("a")},
			Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{
				{Name: "app", RestartCount: 2},
			}},
		},
		{
			ObjectMeta: metav1.ObjectMeta{UID: types.UID("b")},
			Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{
				{Name: "app", RestartCount: 0},
			}},
		},
	}
	baseline := RestartBaseline{"a/app": 1, "b/app": 0}
	// deltas: 1 + 0 = 1; pods = 2; ratio = 0.5
	require.InDelta(t, 0.5, RestartRatio(pods, baseline), 0.0001)

	// New pod with no baseline counts full restarts.
	pods = append(pods, &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{UID: types.UID("c")},
		Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{
			{Name: "app", RestartCount: 1},
		}},
	})
	require.InDelta(t, 2.0/3.0, RestartRatio(pods, baseline), 0.0001)
}

func TestDetectDependencyCycle(t *testing.T) {
	a := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "a", Annotations: map[string]string{config.RolloutDependsOnAnnotationKey: "c"}}}
	b := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "b", Annotations: map[string]string{config.RolloutDependsOnAnnotationKey: "a"}}}
	c := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "c", Annotations: map[string]string{config.RolloutDependsOnAnnotationKey: "b"}}}
	byName := map[string]*appsv1.Deployment{"a": a, "b": b, "c": c}
	require.True(t, DetectDependencyCycle("a", byName))

	c.Annotations = nil
	require.False(t, DetectDependencyCycle("a", byName))
}

func TestGateActive(t *testing.T) {
	d := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Labels: map[string]string{config.RolloutPhasedLabelKey: config.RolloutPhasedLabelValue},
			Annotations: map[string]string{
				config.RolloutDependsOnAnnotationKey:          "upstream",
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

func TestExceedsRestartThreshold(t *testing.T) {
	require.False(t, ExceedsRestartThreshold(0, 0))
	require.True(t, ExceedsRestartThreshold(0.01, 0))
	require.False(t, ExceedsRestartThreshold(0.09, 0.1))
	require.True(t, ExceedsRestartThreshold(0.1, 0.1))
	require.True(t, ExceedsRestartThreshold(0.2, 0.1))
}

func TestEncodeDecodeRestartBaseline(t *testing.T) {
	in := RestartBaseline{"uid/app": 3}
	raw, err := EncodeRestartBaseline(in)
	require.NoError(t, err)
	out, err := DecodeRestartBaseline(raw)
	require.NoError(t, err)
	require.Equal(t, in, out)
}
