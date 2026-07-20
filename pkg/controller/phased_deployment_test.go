package controller

import (
	"context"
	"testing"
	"time"

	"github.com/go-kit/log"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/grafana/rollout-operator/pkg/config"
	"github.com/grafana/rollout-operator/pkg/phased"
)

func TestPhasedDeploymentController_WaitsForUpstreamThenSoaks(t *testing.T) {
	replicas := int32(2)
	upstream := mockPhasedDeployment("zone-a", "", "r1", false, replicas, true)
	follower := mockPhasedDeployment("zone-b", "zone-a", "r1", true, replicas, false)
	follower.Annotations[config.RolloutDependencyPhaseAnnotationKey] = config.RolloutDependencyPhaseWaiting
	follower.Annotations[config.RolloutDependencyRevisionAnnotationKey] = "r1"

	pods := []runtime.Object{
		mockDeploymentPod("zone-a", "zone-a-0", "uid-a0", 0),
		mockDeploymentPod("zone-a", "zone-a-1", "uid-a1", 0),
	}

	api := fake.NewSimpleClientset(append([]runtime.Object{upstream, follower}, pods...)...)
	c := newTestPhasedController(t, api)
	require.NoError(t, c.reconcile(context.Background()))

	follower, err := api.AppsV1().Deployments(testNamespace).Get(context.Background(), "zone-b", metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, config.RolloutDependencyPhaseSoaking, phased.Phase(follower))
	require.True(t, follower.Spec.Paused)
	require.NotEmpty(t, follower.Annotations[config.RolloutSoakStartedAtAnnotationKey])
	require.NotEmpty(t, follower.Annotations[config.RolloutRestartBaselineAnnotationKey])
}

func TestPhasedDeploymentController_CompletesAfterSoakWithLowRestarts(t *testing.T) {
	replicas := int32(2)
	upstream := mockPhasedDeployment("zone-a", "", "r1", false, replicas, true)
	follower := mockPhasedDeployment("zone-b", "zone-a", "r1", true, replicas, false)
	follower.Annotations[config.RolloutDependencyPhaseAnnotationKey] = config.RolloutDependencyPhaseSoaking
	follower.Annotations[config.RolloutDependencyRevisionAnnotationKey] = "r1"
	follower.Annotations[config.RolloutSoakStartedAtAnnotationKey] = time.Now().UTC().Add(-6 * time.Minute).Format(time.RFC3339)
	baseline, err := phased.EncodeRestartBaseline(phased.RestartBaseline{"uid-a0/app": 0, "uid-a1/app": 0})
	require.NoError(t, err)
	follower.Annotations[config.RolloutRestartBaselineAnnotationKey] = baseline
	follower.Annotations[config.RolloutHadPausedAnnotationKey] = phased.HadPausedAnnotationFalse

	pods := []runtime.Object{
		mockDeploymentPod("zone-a", "zone-a-0", "uid-a0", 0),
		mockDeploymentPod("zone-a", "zone-a-1", "uid-a1", 0),
	}
	api := fake.NewSimpleClientset(append([]runtime.Object{upstream, follower}, pods...)...)
	c := newTestPhasedController(t, api)
	require.NoError(t, c.reconcile(context.Background()))

	follower, err = api.AppsV1().Deployments(testNamespace).Get(context.Background(), "zone-b", metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, config.RolloutDependencyPhaseComplete, phased.Phase(follower))
	require.False(t, follower.Spec.Paused)
}

func TestPhasedDeploymentController_BlocksOnHighRestartRatio(t *testing.T) {
	replicas := int32(2)
	upstream := mockPhasedDeployment("zone-a", "", "r1", false, replicas, true)
	follower := mockPhasedDeployment("zone-b", "zone-a", "r1", true, replicas, false)
	follower.Annotations[config.RolloutDependencyPhaseAnnotationKey] = config.RolloutDependencyPhaseSoaking
	follower.Annotations[config.RolloutDependencyRevisionAnnotationKey] = "r1"
	follower.Annotations[config.RolloutSoakStartedAtAnnotationKey] = time.Now().UTC().Add(-6 * time.Minute).Format(time.RFC3339)
	baseline, err := phased.EncodeRestartBaseline(phased.RestartBaseline{"uid-a0/app": 0, "uid-a1/app": 0})
	require.NoError(t, err)
	follower.Annotations[config.RolloutRestartBaselineAnnotationKey] = baseline

	// 1 restart across 2 pods => ratio 0.5 >= 0.1
	pods := []runtime.Object{
		mockDeploymentPod("zone-a", "zone-a-0", "uid-a0", 1),
		mockDeploymentPod("zone-a", "zone-a-1", "uid-a1", 0),
	}
	api := fake.NewSimpleClientset(append([]runtime.Object{upstream, follower}, pods...)...)
	c := newTestPhasedController(t, api)
	require.NoError(t, c.reconcile(context.Background()))

	follower, err = api.AppsV1().Deployments(testNamespace).Get(context.Background(), "zone-b", metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, config.RolloutDependencyPhaseBlocked, phased.Phase(follower))
	require.True(t, follower.Spec.Paused)
}

func TestPhasedDeploymentController_ResumeUnpausesImmediately(t *testing.T) {
	replicas := int32(1)
	upstream := mockPhasedDeployment("zone-a", "", "r1", false, replicas, true)
	follower := mockPhasedDeployment("zone-b", "zone-a", "r1", true, replicas, false)
	follower.Annotations[config.RolloutDependencyPhaseAnnotationKey] = config.RolloutDependencyPhaseBlocked
	follower.Annotations[config.RolloutDependencyRevisionAnnotationKey] = "r1"
	follower.Annotations[config.RolloutResumeAnnotationKey] = "r1"
	follower.Annotations[config.RolloutHadPausedAnnotationKey] = phased.HadPausedAnnotationFalse

	api := fake.NewSimpleClientset(upstream, follower)
	c := newTestPhasedController(t, api)
	require.NoError(t, c.reconcile(context.Background()))

	follower, err := api.AppsV1().Deployments(testNamespace).Get(context.Background(), "zone-b", metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, config.RolloutDependencyPhaseComplete, phased.Phase(follower))
	require.False(t, follower.Spec.Paused)
	require.Empty(t, follower.Annotations[config.RolloutResumeAnnotationKey])
}

func TestPhasedDeploymentController_ResetsSoakWhenUpstreamUnhealthy(t *testing.T) {
	replicas := int32(2)
	upstream := mockPhasedDeployment("zone-a", "", "r1", false, replicas, false) // not fully rolled out
	follower := mockPhasedDeployment("zone-b", "zone-a", "r1", true, replicas, false)
	follower.Annotations[config.RolloutDependencyPhaseAnnotationKey] = config.RolloutDependencyPhaseSoaking
	follower.Annotations[config.RolloutDependencyRevisionAnnotationKey] = "r1"
	follower.Annotations[config.RolloutSoakStartedAtAnnotationKey] = time.Now().UTC().Add(-1 * time.Minute).Format(time.RFC3339)
	follower.Annotations[config.RolloutRestartBaselineAnnotationKey] = `{}`

	api := fake.NewSimpleClientset(upstream, follower)
	c := newTestPhasedController(t, api)
	require.NoError(t, c.reconcile(context.Background()))

	follower, err := api.AppsV1().Deployments(testNamespace).Get(context.Background(), "zone-b", metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, config.RolloutDependencyPhaseWaiting, phased.Phase(follower))
	require.Empty(t, follower.Annotations[config.RolloutSoakStartedAtAnnotationKey])
}

func newTestPhasedController(t *testing.T, api *fake.Clientset) *PhasedDeploymentController {
	t.Helper()
	c := NewPhasedDeploymentController(api, testNamespace, time.Second, prometheus.NewRegistry(), log.NewNopLogger())
	require.NoError(t, c.Init())
	t.Cleanup(c.Stop)
	return c
}

func mockPhasedDeployment(name, dependsOn, revision string, paused bool, replicas int32, fullyRolledOut bool) *appsv1.Deployment {
	labels := map[string]string{
		config.RolloutPhasedLabelKey: config.RolloutPhasedLabelValue,
		"name":                       name,
	}
	ann := map[string]string{
		config.RolloutRevisionAnnotationKey: revision,
	}
	if dependsOn != "" {
		ann[config.RolloutDependsOnAnnotationKey] = dependsOn
	}
	d := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   testNamespace,
			Labels:      labels,
			Annotations: ann,
			Generation:  1,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Paused:   paused,
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"name": name}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"name": name}},
			},
		},
	}
	if fullyRolledOut {
		d.Status = appsv1.DeploymentStatus{
			ObservedGeneration: 1,
			Replicas:           replicas,
			UpdatedReplicas:    replicas,
			ReadyReplicas:      replicas,
			AvailableReplicas:  replicas,
		}
	} else {
		d.Status = appsv1.DeploymentStatus{
			ObservedGeneration: 1,
			Replicas:           replicas,
			UpdatedReplicas:    replicas - 1,
			ReadyReplicas:      replicas - 1,
			AvailableReplicas:  replicas - 1,
		}
	}
	return d
}

func mockDeploymentPod(deployName, podName, uid string, restarts int32) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      podName,
			Namespace: testNamespace,
			UID:       types.UID(uid),
			Labels:    map[string]string{"name": deployName},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			ContainerStatuses: []corev1.ContainerStatus{
				{Name: "app", Ready: true, RestartCount: restarts, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}},
			},
		},
	}
}
