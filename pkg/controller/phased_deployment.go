package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/go-kit/log"
	"github.com/go-kit/log/level"
	"github.com/hashicorp/go-multierror"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"go.uber.org/atomic"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	listersv1 "k8s.io/client-go/listers/apps/v1"
	"k8s.io/client-go/tools/cache"

	"github.com/grafana/rollout-operator/pkg/config"
	"github.com/grafana/rollout-operator/pkg/healthcheck"
	"github.com/grafana/rollout-operator/pkg/phased"
	"github.com/grafana/rollout-operator/pkg/util"
)

const (
	bypassEventReason = "RolloutBypass"
	bypassEventSource = "rollout-operator"
)

// PhasedDeploymentController sequences opted-in Deployments that declare canary dependencies.
type PhasedDeploymentController struct {
	kubeClient          kubernetes.Interface
	namespace           string
	reconcileInterval   time.Duration
	deploymentsFactory  informers.SharedInformerFactory
	deploymentLister    listersv1.DeploymentLister
	deploymentsInformer cache.SharedIndexInformer
	logger              log.Logger
	now                 func() time.Time
	healthGate          HealthGate
	healthMetrics       *healthcheck.Metrics

	shouldReconcile atomic.Bool
	stopCh          chan struct{}

	phase             *prometheus.GaugeVec
	blocked           *prometheus.GaugeVec
	bypassActive      *prometheus.GaugeVec
	bypassTotal       *prometheus.CounterVec
	reconcileTotal    prometheus.Counter
	reconcileFailed   prometheus.Counter
	reconcileDuration prometheus.Histogram
}

func NewPhasedDeploymentController(kubeClient kubernetes.Interface, namespace string, reconcileInterval time.Duration, reg prometheus.Registerer, logger log.Logger) *PhasedDeploymentController {
	namespaceOpt := informers.WithNamespace(namespace)
	deploymentsSel := labels.NewSelector().Add(util.MustNewLabelsRequirement(config.RolloutPhasedLabelKey, selection.Equals, []string{config.RolloutPhasedLabelValue})).String()
	deploymentsSelOpt := informers.WithTweakListOptions(func(options *metav1.ListOptions) {
		options.LabelSelector = deploymentsSel
	})

	deploymentsFactory := informers.NewSharedInformerFactoryWithOptions(kubeClient, informerSyncInterval, namespaceOpt, deploymentsSelOpt)
	deploymentsInformer := deploymentsFactory.Apps().V1().Deployments()

	c := &PhasedDeploymentController{
		kubeClient:          kubeClient,
		namespace:           namespace,
		reconcileInterval:   reconcileInterval,
		deploymentsFactory:  deploymentsFactory,
		deploymentLister:    deploymentsInformer.Lister(),
		deploymentsInformer: deploymentsInformer.Informer(),
		logger:              logger,
		now:                 time.Now,
		stopCh:              make(chan struct{}),
		phase: promauto.With(reg).NewGaugeVec(prometheus.GaugeOpts{
			Name: "rollout_operator_phased_deployment_phase",
			Help: "Current phased Deployment gate phase (1=active for labeled phase).",
		}, []string{"deployment", "phase"}),
		blocked: promauto.With(reg).NewGaugeVec(prometheus.GaugeOpts{
			Name: "rollout_operator_phased_deployment_blocked",
			Help: "Whether a phased Deployment is blocked (1) due to a config error.",
		}, []string{"deployment", "reason"}),
		bypassActive: promauto.With(reg).NewGaugeVec(prometheus.GaugeOpts{
			Name: "rollout_operator_phased_deployment_bypass_active",
			Help: "Whether a time-limited rollout bypass is currently active (1).",
		}, []string{"deployment"}),
		bypassTotal: promauto.With(reg).NewCounterVec(prometheus.CounterOpts{
			Name: "rollout_operator_phased_deployment_bypass_total",
			Help: "Total number of times a phased Deployment used a time-limited rollout bypass.",
		}, []string{"deployment"}),
		reconcileTotal: promauto.With(reg).NewCounter(prometheus.CounterOpts{
			Name: "rollout_operator_phased_deployment_reconciles_total",
			Help: "Total number of phased Deployment reconciles started.",
		}),
		reconcileFailed: promauto.With(reg).NewCounter(prometheus.CounterOpts{
			Name: "rollout_operator_phased_deployment_reconciles_failed_total",
			Help: "Total number of phased Deployment reconciles that failed.",
		}),
		reconcileDuration: promauto.With(reg).NewHistogram(prometheus.HistogramOpts{
			Name:    "rollout_operator_phased_deployment_reconcile_duration_seconds",
			Help:    "Time spent reconciling phased Deployments.",
			Buckets: prometheus.DefBuckets,
		}),
	}
	return c
}

// SetHealthCheck wires optional health-check gating into phased Deployment rollouts.
func (c *PhasedDeploymentController) SetHealthCheck(gate HealthGate, metrics *healthcheck.Metrics) {
	c.healthGate = gate
	c.healthMetrics = metrics
}

func (c *PhasedDeploymentController) Init() error {
	_, err := c.deploymentsInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { c.enqueueReconcile() },
		UpdateFunc: func(old, new interface{}) { c.enqueueReconcile() },
		DeleteFunc: func(obj interface{}) { c.enqueueReconcile() },
	})
	if err != nil {
		return err
	}

	go c.deploymentsFactory.Start(c.stopCh)

	level.Info(c.logger).Log("msg", "phased deployment informer caches are syncing")
	if ok := cache.WaitForCacheSync(c.stopCh, c.deploymentsInformer.HasSynced); !ok {
		return errors.New("phased deployment informer caches failed to sync")
	}
	level.Info(c.logger).Log("msg", "phased deployment informer caches have synced")
	return nil
}

func (c *PhasedDeploymentController) Run() {
	ctx := context.Background()
	for {
		if c.shouldReconcile.CompareAndSwap(true, false) {
			if err := c.reconcile(ctx); err != nil {
				level.Warn(c.logger).Log("msg", "phased deployment reconcile failed", "err", err)
				c.shouldReconcile.Store(true)
			}
		}
		select {
		case <-c.stopCh:
			return
		case <-time.After(c.reconcileInterval):
		}
	}
}

func (c *PhasedDeploymentController) Stop() {
	close(c.stopCh)
}

func (c *PhasedDeploymentController) enqueueReconcile() {
	c.shouldReconcile.Store(true)
}

func (c *PhasedDeploymentController) reconcile(ctx context.Context) error {
	c.reconcileTotal.Inc()
	timer := prometheus.NewTimer(c.reconcileDuration)
	defer timer.ObserveDuration()

	deps, err := c.deploymentLister.Deployments(c.namespace).List(labels.Everything())
	if err != nil {
		c.reconcileFailed.Inc()
		return err
	}

	byName := make(map[string]*appsv1.Deployment, len(deps))
	for _, d := range deps {
		byName[d.Name] = d
	}

	var errs error
	for _, d := range deps {
		if err := c.reconcileDeployment(ctx, d, byName); err != nil {
			errs = multierror.Append(errs, err)
		}
	}
	if errs != nil {
		c.reconcileFailed.Inc()
	}
	return errs
}

func (c *PhasedDeploymentController) reconcileDeployment(ctx context.Context, dep *appsv1.Deployment, byName map[string]*appsv1.Deployment) error {
	canaries := phased.Canaries(dep)
	if len(canaries) == 0 {
		if phased.Phase(dep) != "" || strings.TrimSpace(annotationOrEmpty(dep, config.RolloutHealthCheckAnnotationKey)) != "" {
			c.clearDeploymentHealthGate(ctx, dep)
		}
		c.clearMetrics(dep.Name)
		return nil
	}

	revision := phased.Revision(dep)
	if revision == "" {
		c.setPhaseMetric(dep.Name, "missing_revision")
		c.bypassActive.WithLabelValues(dep.Name).Set(0)
		return nil
	}

	now := c.now()
	if _, _, err := phased.BypassUntil(dep); err != nil {
		level.Warn(c.logger).Log("msg", "invalid rollout-bypass-until, ignoring", "deployment", dep.Name, "err", err)
	}
	if phased.BypassActive(dep, now) {
		c.clearDeploymentHealthGate(ctx, dep)
		c.bypassActive.WithLabelValues(dep.Name).Set(1)
		needsBypassApply := phased.Phase(dep) != config.RolloutDependencyPhaseComplete ||
			phased.DependencyRevision(dep) != revision ||
			(dep.Spec.Paused && annotationOrEmpty(dep, config.RolloutHadPausedAnnotationKey) != phased.HadPausedAnnotationTrue)
		if needsBypassApply {
			level.Info(c.logger).Log("msg", "applying phased deployment bypass", "deployment", dep.Name, "revision", revision)
			if err := c.completeGate(ctx, dep, fmt.Sprintf("bypassed until %s", annotationOrEmpty(dep, config.RolloutBypassUntilAnnotationKey))); err != nil {
				return err
			}
			c.bypassTotal.WithLabelValues(dep.Name).Inc()
			c.emitBypassEvent(ctx, dep, revision)
			return nil
		}
		c.setPhaseMetric(dep.Name, config.RolloutDependencyPhaseComplete)
		c.blocked.DeleteLabelValues(dep.Name, "config")
		return nil
	}
	c.bypassActive.WithLabelValues(dep.Name).Set(0)
	if !c.shouldEvaluateHealth(dep) {
		c.clearDeploymentHealthGate(ctx, dep)
	}

	if phased.Phase(dep) == config.RolloutDependencyPhaseComplete && phased.DependencyRevision(dep) == revision {
		c.setPhaseMetric(dep.Name, config.RolloutDependencyPhaseComplete)
		c.blocked.DeleteLabelValues(dep.Name, "config")
		return nil
	}

	// Ensure gate annotations exist (webhook may not have run yet on CREATE-before-label cases).
	if phased.NeedsNewGate(dep) || phased.Phase(dep) == "" {
		hadPaused := phased.HadPausedAnnotationFalse
		prevHad := annotationOrEmpty(dep, config.RolloutHadPausedAnnotationKey)
		prevPhase := phased.Phase(dep)
		if prevPhase != "" && prevPhase != config.RolloutDependencyPhaseComplete {
			// Carry forward pause intent across revision changes while a gate was active.
			if prevHad != "" {
				hadPaused = prevHad
			}
		} else if dep.Spec.Paused {
			hadPaused = phased.HadPausedAnnotationTrue
		}
		if err := c.patchDeployment(ctx, dep.Name, map[string]interface{}{
			"metadata": map[string]interface{}{
				"annotations": map[string]interface{}{
					config.RolloutDependencyPhaseAnnotationKey:    config.RolloutDependencyPhaseWaiting,
					config.RolloutDependencyRevisionAnnotationKey: revision,
					config.RolloutDependencyReasonAnnotationKey:   "waiting for canary deployment(s)",
					config.RolloutHadPausedAnnotationKey:          hadPaused,
				},
			},
			"spec": map[string]interface{}{
				"paused": true,
			},
		}); err != nil {
			return err
		}
		c.setPhaseMetric(dep.Name, config.RolloutDependencyPhaseWaiting)
		return nil
	}

	if phased.DetectDependencyCycle(dep.Name, byName) {
		c.setPhaseMetric(dep.Name, "config_error")
		c.blocked.WithLabelValues(dep.Name, "config").Set(1)
		return c.ensurePhase(ctx, dep, config.RolloutDependencyPhaseWaiting, "dependency cycle detected")
	}

	canaryDeployments := make([]*appsv1.Deployment, 0, len(canaries))
	var healthBaseline time.Time
	for _, canaryName := range canaries {
		canary, err := c.getCanary(ctx, canaryName, byName)
		if err != nil {
			c.setPhaseMetric(dep.Name, "config_error")
			c.blocked.WithLabelValues(dep.Name, "config").Set(1)
			return c.ensurePhase(ctx, dep, config.RolloutDependencyPhaseWaiting, err.Error())
		}
		if phased.Revision(canary) != revision {
			c.setPhaseMetric(dep.Name, config.RolloutDependencyPhaseWaiting)
			c.blocked.DeleteLabelValues(dep.Name, "config")
			return c.ensurePhase(ctx, dep, config.RolloutDependencyPhaseWaiting, fmt.Sprintf("waiting for canary %q to reach revision %s", canaryName, revision))
		}
		canaryDeployments = append(canaryDeployments, canary)
		if c.shouldEvaluateHealth(dep) {
			startedAt, err := c.ensureDeploymentHealthCheckStartedAt(ctx, canary, revision)
			if err != nil {
				return err
			}
			if healthBaseline.IsZero() || startedAt.Before(healthBaseline) {
				healthBaseline = startedAt
			}
		}
		if !phased.IsFullyRolledOut(canary) {
			c.setPhaseMetric(dep.Name, config.RolloutDependencyPhaseWaiting)
			c.blocked.DeleteLabelValues(dep.Name, "config")
			return c.ensurePhase(ctx, dep, config.RolloutDependencyPhaseWaiting, fmt.Sprintf("waiting for canary %q to become fully rolled out", canaryName))
		}
	}

	level.Info(c.logger).Log(
		"msg", "phased deployment canaries ready",
		"deployment", dep.Name,
		"canaries", strings.Join(canaries, ","),
		"revision", revision,
	)
	c.blocked.DeleteLabelValues(dep.Name, "config")
	if c.shouldEvaluateHealth(dep) {
		pause, reason, err := c.evaluateDeploymentHealthGate(ctx, dep, canaryDeployments, healthBaseline)
		if err != nil {
			return err
		}
		if pause {
			c.setPhaseMetric(dep.Name, config.RolloutDependencyPhaseWaiting)
			return c.ensurePhase(ctx, dep, config.RolloutDependencyPhaseWaiting, reason)
		}
	}
	return c.completeGate(ctx, dep, fmt.Sprintf("canaries ready: %s", strings.Join(canaries, ",")))
}

func (c *PhasedDeploymentController) shouldEvaluateHealth(dep *appsv1.Deployment) bool {
	return c.healthGate != nil && strings.TrimSpace(annotationOrEmpty(dep, config.RolloutHealthCheckAnnotationKey)) != ""
}

func (c *PhasedDeploymentController) clearDeploymentHealthGate(ctx context.Context, dep *appsv1.Deployment) {
	if c.healthGate == nil {
		return
	}
	groupName := deploymentHealthGroup(dep)
	c.healthGate.Evaluate(ctx, healthcheck.Request{
		RolloutGroup:      groupName,
		StateKey:          deploymentHealthStateKey(groupName, dep.Name),
		Namespace:         c.namespace,
		TargetName:        dep.Name,
		TargetKind:        "Deployment",
		TargetLabels:      dep.Labels,
		TargetAnnotations: nil,
		EventTarget:       dep,
	})
}

func (c *PhasedDeploymentController) ensureDeploymentHealthCheckStartedAt(ctx context.Context, dep *appsv1.Deployment, revision string) (time.Time, error) {
	existing := healthcheck.ParseStartedAtAnnotation(annotationOrEmpty(dep, config.RolloutHealthCheckStartedAtAnnotationKey), revision)
	if !existing.IsZero() {
		return existing, nil
	}
	startedAt := c.now().UTC()
	value := healthcheck.FormatStartedAtAnnotation(revision, startedAt)
	if err := c.patchDeployment(ctx, dep.Name, map[string]interface{}{
		"metadata": map[string]interface{}{
			"annotations": map[string]interface{}{
				config.RolloutHealthCheckStartedAtAnnotationKey: value,
			},
		},
	}); err != nil {
		return time.Time{}, fmt.Errorf("failed to patch health-check started-at on Deployment %s: %w", dep.Name, err)
	}
	return startedAt, nil
}

func (c *PhasedDeploymentController) evaluateDeploymentHealthGate(ctx context.Context, dep *appsv1.Deployment, canaries []*appsv1.Deployment, baseline time.Time) (bool, string, error) {
	groupName := deploymentHealthGroup(dep)
	if baseline.IsZero() {
		reason := fmt.Sprintf("health-check baseline timestamp missing for Deployment %s", dep.Name)
		level.Warn(c.logger).Log("msg", reason, "deployment", dep.Name)
		if c.healthMetrics != nil {
			c.healthMetrics.Blocked.WithLabelValues(groupName).Set(1)
		}
		return true, reason, nil
	}

	var candidatePods []*corev1.Pod
	for _, canary := range canaries {
		pods, err := c.listDeploymentPods(ctx, canary)
		if err != nil {
			return false, "", err
		}
		candidatePods = append(candidatePods, pods...)
	}
	stablePods, err := c.listDeploymentPods(ctx, dep)
	if err != nil {
		return false, "", err
	}

	decision := c.healthGate.Evaluate(ctx, healthcheck.Request{
		RolloutGroup:      groupName,
		StateKey:          deploymentHealthStateKey(groupName, dep.Name),
		Namespace:         c.namespace,
		TargetName:        dep.Name,
		TargetKind:        "Deployment",
		TargetLabels:      dep.Labels,
		TargetAnnotations: dep.Annotations,
		EventTarget:       dep,
		CandidatePods:     candidatePods,
		StablePods:        stablePods,
		BaselineTime:      baseline,
		Now:               c.now(),
	})
	return decision.ShouldPause, decision.Reason, nil
}

func deploymentHealthGroup(dep *appsv1.Deployment) string {
	if groupName := dep.Labels[config.RolloutGroupLabelKey]; groupName != "" {
		return groupName
	}
	return dep.Name
}

func deploymentHealthStateKey(groupName, deploymentName string) string {
	return groupName + "/Deployment/" + deploymentName
}

func (c *PhasedDeploymentController) listDeploymentPods(ctx context.Context, dep *appsv1.Deployment) ([]*corev1.Pod, error) {
	selector, err := metav1.LabelSelectorAsSelector(dep.Spec.Selector)
	if err != nil {
		return nil, fmt.Errorf("invalid pod selector for Deployment %s: %w", dep.Name, err)
	}
	podList, err := c.kubeClient.CoreV1().Pods(c.namespace).List(ctx, metav1.ListOptions{LabelSelector: selector.String()})
	if err != nil {
		return nil, fmt.Errorf("failed to list pods for Deployment %s: %w", dep.Name, err)
	}
	pods := make([]*corev1.Pod, 0, len(podList.Items))
	for i := range podList.Items {
		pods = append(pods, &podList.Items[i])
	}
	return pods, nil
}

func (c *PhasedDeploymentController) getCanary(ctx context.Context, name string, byName map[string]*appsv1.Deployment) (*appsv1.Deployment, error) {
	if d, ok := byName[name]; ok {
		return d, nil
	}
	// Canary may lack the phased label; fetch directly.
	d, err := c.kubeClient.AppsV1().Deployments(c.namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("canary deployment %q not found: %w", name, err)
	}
	return d, nil
}

func (c *PhasedDeploymentController) completeGate(ctx context.Context, dep *appsv1.Deployment, reason string) error {
	hadPaused := annotationOrEmpty(dep, config.RolloutHadPausedAnnotationKey) == phased.HadPausedAnnotationTrue
	paused := hadPaused
	c.setPhaseMetric(dep.Name, config.RolloutDependencyPhaseComplete)
	c.blocked.DeleteLabelValues(dep.Name, "config")

	annotations := map[string]interface{}{
		config.RolloutDependencyPhaseAnnotationKey:    config.RolloutDependencyPhaseComplete,
		config.RolloutDependencyRevisionAnnotationKey: phased.Revision(dep),
		config.RolloutDependencyReasonAnnotationKey:   reason,
	}
	return c.patchDeployment(ctx, dep.Name, map[string]interface{}{
		"metadata": map[string]interface{}{
			"annotations": annotations,
		},
		"spec": map[string]interface{}{
			"paused": paused,
		},
	})
}

func (c *PhasedDeploymentController) ensurePhase(ctx context.Context, dep *appsv1.Deployment, phase, reason string) error {
	if phased.Phase(dep) == phase && annotationOrEmpty(dep, config.RolloutDependencyReasonAnnotationKey) == reason && dep.Spec.Paused {
		return nil
	}
	return c.patchDeployment(ctx, dep.Name, map[string]interface{}{
		"metadata": map[string]interface{}{
			"annotations": map[string]interface{}{
				config.RolloutDependencyPhaseAnnotationKey:  phase,
				config.RolloutDependencyReasonAnnotationKey: reason,
			},
		},
		"spec": map[string]interface{}{
			"paused": true,
		},
	})
}

func (c *PhasedDeploymentController) patchDeployment(ctx context.Context, name string, patch map[string]interface{}) error {
	b, err := json.Marshal(patch)
	if err != nil {
		return err
	}
	_, err = c.kubeClient.AppsV1().Deployments(c.namespace).Patch(ctx, name, types.MergePatchType, b, metav1.PatchOptions{
		FieldManager: "rollout-operator",
	})
	return err
}

func (c *PhasedDeploymentController) emitBypassEvent(ctx context.Context, dep *appsv1.Deployment, revision string) {
	until := annotationOrEmpty(dep, config.RolloutBypassUntilAnnotationKey)
	msg := fmt.Sprintf("phased rollout bypassed for revision %s until %s", revision, until)
	event := &corev1.Event{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s.bypass.%d", dep.Name, c.now().UnixNano()),
			Namespace: c.namespace,
		},
		InvolvedObject: corev1.ObjectReference{
			Kind:            "Deployment",
			Namespace:       dep.Namespace,
			Name:            dep.Name,
			UID:             dep.UID,
			APIVersion:      "apps/v1",
			ResourceVersion: dep.ResourceVersion,
		},
		Reason:  bypassEventReason,
		Message: msg,
		Source:  corev1.EventSource{Component: bypassEventSource},
		Type:    corev1.EventTypeWarning,
		Count:   1,
		FirstTimestamp: metav1.Time{
			Time: c.now(),
		},
		LastTimestamp: metav1.Time{
			Time: c.now(),
		},
	}
	if _, err := c.kubeClient.CoreV1().Events(c.namespace).Create(ctx, event, metav1.CreateOptions{}); err != nil {
		level.Warn(c.logger).Log("msg", "failed to emit phased rollout bypass event", "deployment", dep.Name, "err", err)
	}
}

func (c *PhasedDeploymentController) setPhaseMetric(name, phase string) {
	for _, p := range []string{
		config.RolloutDependencyPhaseWaiting,
		config.RolloutDependencyPhaseComplete,
		"config_error",
		"missing_revision",
	} {
		if p == phase {
			c.phase.WithLabelValues(name, p).Set(1)
		} else {
			c.phase.WithLabelValues(name, p).Set(0)
		}
	}
}

func (c *PhasedDeploymentController) clearMetrics(name string) {
	c.phase.DeletePartialMatch(prometheus.Labels{"deployment": name})
	c.blocked.DeletePartialMatch(prometheus.Labels{"deployment": name})
	c.bypassActive.DeleteLabelValues(name)
}

func annotationOrEmpty(dep *appsv1.Deployment, key string) string {
	if dep.Annotations == nil {
		return ""
	}
	return dep.Annotations[key]
}
