package controller

import (
	"bytes"
	"context"
	"strings"
	"sync"
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
	"github.com/grafana/rollout-operator/pkg/healthcheck"
	"github.com/grafana/rollout-operator/pkg/phased"
)

type phasedRequeueGate struct {
	mu        sync.Mutex
	decisions []healthcheck.Decision
	calls     int
	called    chan struct{}
}

func (g *phasedRequeueGate) Evaluate(_ context.Context, _ healthcheck.Request) healthcheck.Decision {
	g.mu.Lock()
	defer g.mu.Unlock()
	index := min(g.calls, len(g.decisions)-1)
	decision := g.decisions[index]
	g.calls++
	select {
	case g.called <- struct{}{}:
	default:
	}
	return decision
}

func (g *phasedRequeueGate) callCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.calls
}

func TestPhasedDeploymentController_WaitsForCanary(t *testing.T) {
	replicas := int32(2)
	canary := mockPhasedDeployment("zone-a", "", "r1", false, replicas, false)
	main := mockPhasedDeployment("zone-b", "zone-a", "r1", true, replicas, false)
	main.Annotations[config.RolloutDependencyPhaseAnnotationKey] = config.RolloutDependencyPhaseWaiting
	main.Annotations[config.RolloutDependencyRevisionAnnotationKey] = "r1"

	api := fake.NewSimpleClientset(canary, main)
	var logs bytes.Buffer
	c := newTestPhasedControllerWithLogger(t, api, log.NewLogfmtLogger(&logs))
	require.NoError(t, c.reconcile(context.Background()))

	main, err := api.AppsV1().Deployments(testNamespace).Get(context.Background(), "zone-b", metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, config.RolloutDependencyPhaseWaiting, phased.Phase(main))
	require.True(t, main.Spec.Paused)
	require.Contains(t, main.Annotations[config.RolloutDependencyReasonAnnotationKey], "fully rolled out")
	require.Contains(t, logs.String(), `msg="checking phased deployment canary"`)
	require.Contains(t, logs.String(), "current_revision=r1")
	require.Contains(t, logs.String(), "target_revision=r1")
	require.Contains(t, logs.String(), "ready=false")
	require.True(t, strings.Contains(logs.String(), `msg="holding phased deployment"`))
}

func TestPhasedDeploymentController_CompletesWhenCanaryReady(t *testing.T) {
	replicas := int32(2)
	canary := mockPhasedDeployment("zone-a", "", "r1", false, replicas, true)
	main := mockPhasedDeployment("zone-b", "zone-a", "r1", true, replicas, false)
	main.Annotations[config.RolloutDependencyPhaseAnnotationKey] = config.RolloutDependencyPhaseWaiting
	main.Annotations[config.RolloutDependencyRevisionAnnotationKey] = "r1"
	main.Annotations[config.RolloutHadPausedAnnotationKey] = phased.HadPausedAnnotationFalse

	api := fake.NewSimpleClientset(canary, main)
	var logs bytes.Buffer
	c := newTestPhasedControllerWithLogger(t, api, log.NewLogfmtLogger(&logs))
	require.NoError(t, c.reconcile(context.Background()))

	main, err := api.AppsV1().Deployments(testNamespace).Get(context.Background(), "zone-b", metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, config.RolloutDependencyPhaseComplete, phased.Phase(main))
	require.False(t, main.Spec.Paused)
	require.Contains(t, logs.String(), `msg="phased deployment canaries ready"`)
	require.Contains(t, logs.String(), `msg="completing phased deployment gate"`)
	require.Contains(t, logs.String(), "action=unpause-and-proceed")
}

func TestPhasedDeploymentController_HealthCheckGatesReadyCanary(t *testing.T) {
	for _, tc := range []struct {
		name        string
		shouldPause bool
		wantPhase   string
		wantPaused  bool
	}{
		{name: "blocked", shouldPause: true, wantPhase: config.RolloutDependencyPhaseWaiting, wantPaused: true},
		{name: "passing", shouldPause: false, wantPhase: config.RolloutDependencyPhaseComplete, wantPaused: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			replicas := int32(1)
			canary := mockPhasedDeployment("zone-a", "", "r1", false, replicas, true)
			main := mockPhasedDeployment("zone-b", "zone-a", "r1", true, replicas, false)
			main.Annotations[config.RolloutDependencyPhaseAnnotationKey] = config.RolloutDependencyPhaseWaiting
			main.Annotations[config.RolloutDependencyRevisionAnnotationKey] = "r1"
			main.Annotations[config.RolloutHadPausedAnnotationKey] = phased.HadPausedAnnotationFalse
			main.Annotations[config.RolloutHealthCheckAnnotationKey] = "deployment-health"

			candidatePod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "zone-a-0", Namespace: testNamespace, Labels: map[string]string{"name": "zone-a"}}}
			stablePod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "zone-b-0", Namespace: testNamespace, Labels: map[string]string{"name": "zone-b"}}}
			api := fake.NewSimpleClientset(canary, main, candidatePod, stablePod)
			c := newTestPhasedController(t, api)
			gate := &mockHealthGate{shouldPause: tc.shouldPause}
			c.SetHealthCheck(gate, nil)

			require.NoError(t, c.reconcile(context.Background()))

			main, err := api.AppsV1().Deployments(testNamespace).Get(context.Background(), "zone-b", metav1.GetOptions{})
			require.NoError(t, err)
			require.Equal(t, tc.wantPhase, phased.Phase(main))
			require.Equal(t, tc.wantPaused, main.Spec.Paused)
			require.Equal(t, 1, gate.callCount())
			require.Equal(t, "zone-b", gate.lastReq.TargetName)
			require.Equal(t, "Deployment", gate.lastReq.TargetKind)
			require.Len(t, gate.lastReq.CandidatePods, 1)
			require.Len(t, gate.lastReq.StablePods, 1)

			canary, err = api.AppsV1().Deployments(testNamespace).Get(context.Background(), "zone-a", metav1.GetOptions{})
			require.NoError(t, err)
			require.NotEmpty(t, canary.Annotations[config.RolloutHealthCheckStartedAtAnnotationKey])
		})
	}
}

func TestPhasedDeploymentController_HealthDeadlineTriggersReconcile(t *testing.T) {
	replicas := int32(1)
	canary := mockPhasedDeployment("zone-a", "", "r1", false, replicas, true)
	canary.Annotations[config.RolloutHealthCheckStartedAtAnnotationKey] = healthcheck.FormatStartedAtAnnotation("r1", time.Now().Add(-time.Minute))
	main := mockPhasedDeployment("zone-b", "zone-a", "r1", true, replicas, false)
	main.Annotations[config.RolloutDependencyPhaseAnnotationKey] = config.RolloutDependencyPhaseWaiting
	main.Annotations[config.RolloutDependencyRevisionAnnotationKey] = "r1"
	main.Annotations[config.RolloutDependencyReasonAnnotationKey] = "blocked by health"
	main.Annotations[config.RolloutHadPausedAnnotationKey] = phased.HadPausedAnnotationFalse
	main.Annotations[config.RolloutHealthCheckAnnotationKey] = "deployment-health"

	candidatePod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "zone-a-0", Namespace: testNamespace, Labels: map[string]string{"name": "zone-a"}}}
	stablePod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "zone-b-0", Namespace: testNamespace, Labels: map[string]string{"name": "zone-b"}}}
	api := fake.NewSimpleClientset(canary, main, candidatePod, stablePod)
	var logs bytes.Buffer
	c := newTestPhasedControllerWithLogger(t, api, log.NewLogfmtLogger(&logs))
	c.reconcileInterval = time.Hour
	gate := &phasedRequeueGate{
		decisions: []healthcheck.Decision{
			{ShouldPause: true, Reason: "blocked by health", RequeueAfter: 25 * time.Millisecond},
			{},
		},
		called: make(chan struct{}, 2),
	}
	c.SetHealthCheck(gate, nil)

	require.NoError(t, c.reconcile(context.Background()))
	require.Equal(t, 1, gate.callCount())
	c.shouldReconcile.Store(false)

	runDone := make(chan struct{})
	go func() {
		c.Run()
		close(runDone)
	}()

	select {
	case <-gate.called:
	case <-time.After(time.Second):
		t.Fatal("health deadline did not trigger a phased Deployment reconcile")
	}
	if gate.callCount() < 2 {
		select {
		case <-gate.called:
		case <-time.After(time.Second):
			t.Fatal("health deadline did not trigger a second gate evaluation")
		}
	}
	require.GreaterOrEqual(t, gate.callCount(), 2)

	c.Stop()
	select {
	case <-runDone:
	case <-time.After(time.Second):
		t.Fatal("phased Deployment controller did not stop")
	}
	require.Contains(t, logs.String(), `msg="scheduled phased deployment health check reevaluation"`)
	require.Contains(t, logs.String(), "deployment=zone-b")
	require.Contains(t, logs.String(), "policy=deployment-health")
}

func TestPhasedDeploymentController_HealthRequeueKeepsEarliestDeadline(t *testing.T) {
	var logs bytes.Buffer
	c := &PhasedDeploymentController{
		logger:       log.NewLogfmtLogger(&logs),
		stopCh:       make(chan struct{}),
		healthWakeCh: make(chan struct{}, 1),
	}
	t.Cleanup(c.Stop)

	c.scheduleHealthRequeue(2*time.Second, "zone-b", "slow")
	c.scheduleHealthRequeue(5*time.Second, "zone-c", "later")
	c.scheduleHealthRequeue(500*time.Millisecond, "zone-d", "fast")

	c.healthTimerMu.Lock()
	timerScheduled := c.healthTimer != nil
	timerDelay := time.Until(c.healthTimerAt)
	c.healthTimerMu.Unlock()
	require.True(t, timerScheduled)
	require.InDelta(t, 500*time.Millisecond, timerDelay, float64(100*time.Millisecond))
	require.Contains(t, logs.String(), "deployment=zone-d")
	require.Contains(t, logs.String(), "policy=fast")
	require.NotContains(t, logs.String(), "policy=later")
}

func TestPhasedDeploymentController_ClearsHealthStateWhenCanaryRemoved(t *testing.T) {
	replicas := int32(1)
	dep := mockPhasedDeployment("zone-b", "", "r1", false, replicas, true)
	dep.Annotations[config.RolloutHealthCheckAnnotationKey] = "deployment-health"

	api := fake.NewSimpleClientset(dep)
	c := newTestPhasedController(t, api)
	gate := &mockHealthGate{}
	c.SetHealthCheck(gate, nil)

	require.NoError(t, c.reconcile(context.Background()))
	require.Equal(t, 1, gate.callCount())
	require.Empty(t, gate.lastReq.TargetAnnotations)
	require.Equal(t, "zone-b/Deployment/zone-b", gate.lastReq.StateKey)
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
	c = newTestPhasedController(t, api)
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

	c = newTestPhasedController(t, api)
	require.NoError(t, c.reconcile(context.Background()))
	events, err = api.CoreV1().Events(testNamespace).List(context.Background(), metav1.ListOptions{})
	require.NoError(t, err)
	require.Len(t, events.Items, 1)
}

func newTestPhasedController(t *testing.T, api *fake.Clientset) *PhasedDeploymentController {
	return newTestPhasedControllerWithLogger(t, api, log.NewNopLogger())
}

func newTestPhasedControllerWithLogger(t *testing.T, api *fake.Clientset, logger log.Logger) *PhasedDeploymentController {
	t.Helper()
	c := NewPhasedDeploymentController(api, testNamespace, time.Second, prometheus.NewRegistry(), logger)
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
