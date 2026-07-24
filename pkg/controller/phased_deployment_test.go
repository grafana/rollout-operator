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
	"k8s.io/client-go/kubernetes/fake"

	"github.com/grafana/rollout-operator/pkg/config"
	"github.com/grafana/rollout-operator/pkg/phased"
)

func TestPhasedDeploymentController_WaitsForCanary(t *testing.T) {
	replicas := int32(2)
	canary := mockPhasedDeployment("zone-a", "", "r1", false, replicas, false)
	main := mockPhasedDeployment("zone-b", "zone-a", "r1", true, replicas, false)
	main.Annotations[config.RolloutDependencyPhaseAnnotationKey] = config.RolloutDependencyPhaseWaiting
	main.Annotations[config.RolloutDependencyRevisionAnnotationKey] = "r1"

	api := fake.NewSimpleClientset(canary, main)
	c := newTestPhasedController(t, api)
	require.NoError(t, c.reconcile(context.Background()))

	main, err := api.AppsV1().Deployments(testNamespace).Get(context.Background(), "zone-b", metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, config.RolloutDependencyPhaseWaiting, phased.Phase(main))
	require.True(t, main.Spec.Paused)
	require.Contains(t, main.Annotations[config.RolloutDependencyReasonAnnotationKey], "fully rolled out")
}

func TestPhasedDeploymentController_CompletesWhenCanaryReady(t *testing.T) {
	replicas := int32(2)
	canary := mockPhasedDeployment("zone-a", "", "r1", false, replicas, true)
	main := mockPhasedDeployment("zone-b", "zone-a", "r1", true, replicas, false)
	main.Annotations[config.RolloutDependencyPhaseAnnotationKey] = config.RolloutDependencyPhaseWaiting
	main.Annotations[config.RolloutDependencyRevisionAnnotationKey] = "r1"
	main.Annotations[config.RolloutHadPausedAnnotationKey] = phased.HadPausedAnnotationFalse

	api := fake.NewSimpleClientset(canary, main)
	c := newTestPhasedController(t, api)
	require.NoError(t, c.reconcile(context.Background()))

	main, err := api.AppsV1().Deployments(testNamespace).Get(context.Background(), "zone-b", metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, config.RolloutDependencyPhaseComplete, phased.Phase(main))
	require.False(t, main.Spec.Paused)
}

func TestPhasedDeploymentController_WaitsForAllCanaries(t *testing.T) {
	replicas := int32(1)
	qf := mockPhasedDeployment("query-frontend-zone-a", "", "r1", false, replicas, true)
	querier := mockPhasedDeployment("querier-zone-a", "", "r1", false, replicas, false)
	main := mockPhasedDeployment("querier-zone-b", "querier-zone-a,query-frontend-zone-a", "r1", true, replicas, false)
	main.Annotations[config.RolloutDependencyPhaseAnnotationKey] = config.RolloutDependencyPhaseWaiting
	main.Annotations[config.RolloutDependencyRevisionAnnotationKey] = "r1"

	api := fake.NewSimpleClientset(qf, querier, main)
	c := newTestPhasedController(t, api)
	require.NoError(t, c.reconcile(context.Background()))

	main, err := api.AppsV1().Deployments(testNamespace).Get(context.Background(), "querier-zone-b", metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, config.RolloutDependencyPhaseWaiting, phased.Phase(main))
	require.Contains(t, main.Annotations[config.RolloutDependencyReasonAnnotationKey], "querier-zone-a")

	querier.Status.UpdatedReplicas = replicas
	querier.Status.ReadyReplicas = replicas
	querier.Status.AvailableReplicas = replicas
	_, err = api.AppsV1().Deployments(testNamespace).Update(context.Background(), querier, metav1.UpdateOptions{})
	require.NoError(t, err)
	// Refresh informer by waiting briefly and re-init is heavy; reconcile uses lister which may be stale.
	// Force a fresh controller against updated API.
	c = newTestPhasedController(t, api)
	require.NoError(t, c.reconcile(context.Background()))

	main, err = api.AppsV1().Deployments(testNamespace).Get(context.Background(), "querier-zone-b", metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, config.RolloutDependencyPhaseComplete, phased.Phase(main))
	require.False(t, main.Spec.Paused)
}

func TestPhasedDeploymentController_BypassUnpauses(t *testing.T) {
	replicas := int32(1)
	canary := mockPhasedDeployment("zone-a", "", "r1", false, replicas, false)
	main := mockPhasedDeployment("zone-b", "zone-a", "r1", true, replicas, false)
	main.Annotations[config.RolloutDependencyPhaseAnnotationKey] = config.RolloutDependencyPhaseWaiting
	main.Annotations[config.RolloutDependencyRevisionAnnotationKey] = "r1"
	main.Annotations[config.RolloutHadPausedAnnotationKey] = phased.HadPausedAnnotationFalse
	main.Annotations[config.RolloutBypassUntilAnnotationKey] = time.Now().UTC().Add(time.Hour).Format(time.RFC3339)

	api := fake.NewSimpleClientset(canary, main)
	c := newTestPhasedController(t, api)
	require.NoError(t, c.reconcile(context.Background()))

	main, err := api.AppsV1().Deployments(testNamespace).Get(context.Background(), "zone-b", metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, config.RolloutDependencyPhaseComplete, phased.Phase(main))
	require.False(t, main.Spec.Paused)

	events, err := api.CoreV1().Events(testNamespace).List(context.Background(), metav1.ListOptions{})
	require.NoError(t, err)
	require.Len(t, events.Items, 1)
	require.Equal(t, bypassEventReason, events.Items[0].Reason)

	// Second reconcile must not re-emit bypass telemetry.
	require.NoError(t, c.reconcile(context.Background()))
	events, err = api.CoreV1().Events(testNamespace).List(context.Background(), metav1.ListOptions{})
	require.NoError(t, err)
	require.Len(t, events.Items, 1)
}

func TestPhasedDeploymentController_BypassHonorsPreExistingPause(t *testing.T) {
	replicas := int32(1)
	canary := mockPhasedDeployment("zone-a", "", "r1", false, replicas, false)
	main := mockPhasedDeployment("zone-b", "zone-a", "r1", true, replicas, false)
	main.Annotations[config.RolloutDependencyPhaseAnnotationKey] = config.RolloutDependencyPhaseWaiting
	main.Annotations[config.RolloutDependencyRevisionAnnotationKey] = "r1"
	main.Annotations[config.RolloutHadPausedAnnotationKey] = phased.HadPausedAnnotationTrue
	main.Annotations[config.RolloutBypassUntilAnnotationKey] = time.Now().UTC().Add(time.Hour).Format(time.RFC3339)

	api := fake.NewSimpleClientset(canary, main)
	c := newTestPhasedController(t, api)
	require.NoError(t, c.reconcile(context.Background()))

	main, err := api.AppsV1().Deployments(testNamespace).Get(context.Background(), "zone-b", metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, config.RolloutDependencyPhaseComplete, phased.Phase(main))
	require.True(t, main.Spec.Paused)

	events, err := api.CoreV1().Events(testNamespace).List(context.Background(), metav1.ListOptions{})
	require.NoError(t, err)
	require.Len(t, events.Items, 1)

	require.NoError(t, c.reconcile(context.Background()))
	events, err = api.CoreV1().Events(testNamespace).List(context.Background(), metav1.ListOptions{})
	require.NoError(t, err)
	require.Len(t, events.Items, 1)
}

func newTestPhasedController(t *testing.T, api *fake.Clientset) *PhasedDeploymentController {
	t.Helper()
	c := NewPhasedDeploymentController(api, testNamespace, time.Second, prometheus.NewRegistry(), log.NewNopLogger())
	require.NoError(t, c.Init())
	t.Cleanup(c.Stop)
	return c
}

func mockPhasedDeployment(name, canaries, revision string, paused bool, replicas int32, fullyRolledOut bool) *appsv1.Deployment {
	labels := map[string]string{
		config.RolloutPhasedLabelKey: config.RolloutPhasedLabelValue,
		"name":                       name,
	}
	ann := map[string]string{
		config.RolloutRevisionAnnotationKey: revision,
	}
	if canaries != "" {
		ann[config.RolloutCanaryAnnotationKey] = canaries
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
