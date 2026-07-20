package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	corelisters "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"

	"github.com/grafana/rollout-operator/pkg/config"
	"github.com/grafana/rollout-operator/pkg/phased"
	"github.com/grafana/rollout-operator/pkg/util"
)

// PhasedDeploymentController sequences opted-in Deployments that declare a depends-on relationship.
type PhasedDeploymentController struct {
	kubeClient          kubernetes.Interface
	namespace           string
	reconcileInterval   time.Duration
	deploymentsFactory  informers.SharedInformerFactory
	deploymentLister    listersv1.DeploymentLister
	deploymentsInformer cache.SharedIndexInformer
	podsFactory         informers.SharedInformerFactory
	podLister           corelisters.PodLister
	podsInformer        cache.SharedIndexInformer
	logger              log.Logger

	shouldReconcile atomic.Bool
	stopCh          chan struct{}

	phase             *prometheus.GaugeVec
	blocked           *prometheus.GaugeVec
	restartRatio      *prometheus.GaugeVec
	soakRemaining     *prometheus.GaugeVec
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
	podsFactory := informers.NewSharedInformerFactoryWithOptions(kubeClient, informerSyncInterval, namespaceOpt)
	podsInformer := podsFactory.Core().V1().Pods()

	c := &PhasedDeploymentController{
		kubeClient:          kubeClient,
		namespace:           namespace,
		reconcileInterval:   reconcileInterval,
		deploymentsFactory:  deploymentsFactory,
		deploymentLister:    deploymentsInformer.Lister(),
		deploymentsInformer: deploymentsInformer.Informer(),
		podsFactory:         podsFactory,
		podLister:           podsInformer.Lister(),
		podsInformer:        podsInformer.Informer(),
		logger:              logger,
		stopCh:              make(chan struct{}),
		phase: promauto.With(reg).NewGaugeVec(prometheus.GaugeOpts{
			Name: "rollout_operator_phased_deployment_phase",
			Help: "Current phased Deployment gate phase (1=active for labeled phase).",
		}, []string{"deployment", "phase"}),
		blocked: promauto.With(reg).NewGaugeVec(prometheus.GaugeOpts{
			Name: "rollout_operator_phased_deployment_blocked",
			Help: "Whether a phased Deployment is blocked (1) awaiting resume.",
		}, []string{"deployment", "reason"}),
		restartRatio: promauto.With(reg).NewGaugeVec(prometheus.GaugeOpts{
			Name: "rollout_operator_phased_deployment_restart_ratio",
			Help: "Measured container-restart ratio at the end of the last soak evaluation.",
		}, []string{"deployment"}),
		soakRemaining: promauto.With(reg).NewGaugeVec(prometheus.GaugeOpts{
			Name: "rollout_operator_phased_deployment_soak_remaining_seconds",
			Help: "Seconds remaining in the current soak window, if soaking.",
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

func (c *PhasedDeploymentController) Init() error {
	_, err := c.deploymentsInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { c.enqueueReconcile() },
		UpdateFunc: func(old, new interface{}) { c.enqueueReconcile() },
		DeleteFunc: func(obj interface{}) { c.enqueueReconcile() },
	})
	if err != nil {
		return err
	}
	_, err = c.podsInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		UpdateFunc: func(old, new interface{}) { c.enqueueReconcile() },
		AddFunc:    func(obj interface{}) { c.enqueueReconcile() },
		DeleteFunc: func(obj interface{}) { c.enqueueReconcile() },
	})
	if err != nil {
		return err
	}

	go c.deploymentsFactory.Start(c.stopCh)
	go c.podsFactory.Start(c.stopCh)

	level.Info(c.logger).Log("msg", "phased deployment informer caches are syncing")
	if ok := cache.WaitForCacheSync(c.stopCh, c.deploymentsInformer.HasSynced, c.podsInformer.HasSynced); !ok {
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
	dependsOn := phased.DependsOn(dep)
	if dependsOn == "" {
		c.clearMetrics(dep.Name)
		return nil
	}

	revision := phased.Revision(dep)
	if revision == "" {
		c.setPhaseMetric(dep.Name, "missing_revision")
		return nil
	}

	// Explicit resume for a blocked revision bypasses the failed soak immediately.
	if phased.ResumeRevision(dep) == revision && phased.Phase(dep) == config.RolloutDependencyPhaseBlocked {
		level.Info(c.logger).Log("msg", "resuming phased deployment after explicit resume annotation", "deployment", dep.Name, "revision", revision)
		return c.completeGate(ctx, dep, "resumed by annotation")
	}

	if phased.Phase(dep) == config.RolloutDependencyPhaseComplete && phased.DependencyRevision(dep) == revision {
		c.setPhaseMetric(dep.Name, config.RolloutDependencyPhaseComplete)
		c.blocked.DeleteLabelValues(dep.Name, "restarts")
		c.blocked.DeleteLabelValues(dep.Name, "config")
		c.soakRemaining.DeleteLabelValues(dep.Name)
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
					config.RolloutDependencyReasonAnnotationKey:   "waiting for upstream deployment",
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

	upstream, ok := byName[dependsOn]
	if !ok {
		// Upstream may lack the phased label; fetch directly.
		u, err := c.kubeClient.AppsV1().Deployments(c.namespace).Get(ctx, dependsOn, metav1.GetOptions{})
		if err != nil {
			c.setPhaseMetric(dep.Name, "config_error")
			c.blocked.WithLabelValues(dep.Name, "config").Set(1)
			return c.ensurePhase(ctx, dep, config.RolloutDependencyPhaseWaiting, fmt.Sprintf("upstream deployment %q not found", dependsOn))
		}
		upstream = u
	}

	if phased.Revision(upstream) != revision {
		c.setPhaseMetric(dep.Name, config.RolloutDependencyPhaseWaiting)
		c.soakRemaining.WithLabelValues(dep.Name).Set(0)
		c.blocked.DeleteLabelValues(dep.Name, "config")
		return c.ensurePhase(ctx, dep, config.RolloutDependencyPhaseWaiting, fmt.Sprintf("waiting for upstream %q to reach revision %s", dependsOn, revision))
	}

	if !phased.IsFullyRolledOut(upstream) {
		// Upstream became unhealthy during soak — reset soak when we next see it healthy.
		if phased.Phase(dep) == config.RolloutDependencyPhaseSoaking {
			level.Info(c.logger).Log("msg", "upstream no longer fully rolled out during soak; resetting soak", "deployment", dep.Name, "upstream", dependsOn)
			return c.resetSoak(ctx, dep, fmt.Sprintf("waiting for upstream %q to become fully rolled out", dependsOn))
		}
		c.setPhaseMetric(dep.Name, config.RolloutDependencyPhaseWaiting)
		c.blocked.DeleteLabelValues(dep.Name, "config")
		return c.ensurePhase(ctx, dep, config.RolloutDependencyPhaseWaiting, fmt.Sprintf("waiting for upstream %q to become fully rolled out", dependsOn))
	}

	soakDuration, err := phased.ParseSoakDuration(dep.Annotations)
	if err != nil {
		c.setPhaseMetric(dep.Name, "config_error")
		c.blocked.WithLabelValues(dep.Name, "config").Set(1)
		return c.ensurePhase(ctx, dep, config.RolloutDependencyPhaseWaiting, err.Error())
	}
	threshold, err := phased.ParseRestartThreshold(dep.Annotations)
	if err != nil {
		c.setPhaseMetric(dep.Name, "config_error")
		c.blocked.WithLabelValues(dep.Name, "config").Set(1)
		return c.ensurePhase(ctx, dep, config.RolloutDependencyPhaseWaiting, err.Error())
	}

	// Restart failures stay blocked until an explicit resume for this revision.
	if phased.Phase(dep) == config.RolloutDependencyPhaseBlocked {
		c.setPhaseMetric(dep.Name, config.RolloutDependencyPhaseBlocked)
		c.blocked.WithLabelValues(dep.Name, "restarts").Set(1)
		c.blocked.DeleteLabelValues(dep.Name, "config")
		c.soakRemaining.WithLabelValues(dep.Name).Set(0)
		return c.ensurePaused(ctx, dep)
	}

	soakStartedAt, hasSoak := parseSoakStartedAt(dep)
	if !hasSoak || phased.Phase(dep) != config.RolloutDependencyPhaseSoaking {
		return c.startSoak(ctx, dep, upstream)
	}

	remaining := soakDuration - time.Since(soakStartedAt)
	if remaining > 0 {
		c.setPhaseMetric(dep.Name, config.RolloutDependencyPhaseSoaking)
		c.soakRemaining.WithLabelValues(dep.Name).Set(remaining.Seconds())
		return c.ensurePaused(ctx, dep)
	}
	c.soakRemaining.WithLabelValues(dep.Name).Set(0)

	// Soak finished: evaluate restart ratio once.
	pods, err := c.listDeploymentPods(upstream)
	if err != nil {
		return err
	}
	baseline, err := phased.DecodeRestartBaseline(annotationOrEmpty(dep, config.RolloutRestartBaselineAnnotationKey))
	if err != nil {
		return fmt.Errorf("decode restart baseline for %s: %w", dep.Name, err)
	}
	ratio := phased.RestartRatio(pods, baseline)
	c.restartRatio.WithLabelValues(dep.Name).Set(ratio)

	if phased.ExceedsRestartThreshold(ratio, threshold) {
		level.Warn(c.logger).Log(
			"msg", "phased deployment blocked by restart threshold",
			"deployment", dep.Name,
			"upstream", dependsOn,
			"restart_ratio", ratio,
			"threshold", threshold,
		)
		c.setPhaseMetric(dep.Name, config.RolloutDependencyPhaseBlocked)
		c.blocked.WithLabelValues(dep.Name, "restarts").Set(1)
		return c.patchDeployment(ctx, dep.Name, map[string]interface{}{
			"metadata": map[string]interface{}{
				"annotations": map[string]interface{}{
					config.RolloutDependencyPhaseAnnotationKey:  config.RolloutDependencyPhaseBlocked,
					config.RolloutDependencyReasonAnnotationKey: fmt.Sprintf("restart ratio %.4f >= threshold %.4f", ratio, threshold),
				},
			},
			"spec": map[string]interface{}{
				"paused": true,
			},
		})
	}

	level.Info(c.logger).Log(
		"msg", "phased deployment soak passed",
		"deployment", dep.Name,
		"upstream", dependsOn,
		"restart_ratio", ratio,
		"threshold", threshold,
	)
	c.blocked.DeleteLabelValues(dep.Name, "restarts")
	return c.completeGate(ctx, dep, fmt.Sprintf("soak passed (restart ratio %.4f)", ratio))
}

func (c *PhasedDeploymentController) startSoak(ctx context.Context, dep, upstream *appsv1.Deployment) error {
	pods, err := c.listDeploymentPods(upstream)
	if err != nil {
		return err
	}
	baseline, err := phased.EncodeRestartBaseline(phased.BuildRestartBaseline(pods))
	if err != nil {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	level.Info(c.logger).Log("msg", "starting phased deployment soak", "deployment", dep.Name, "upstream", upstream.Name, "pods", len(pods))
	c.setPhaseMetric(dep.Name, config.RolloutDependencyPhaseSoaking)
	c.blocked.DeleteLabelValues(dep.Name, "config")
	return c.patchDeployment(ctx, dep.Name, map[string]interface{}{
		"metadata": map[string]interface{}{
			"annotations": map[string]interface{}{
				config.RolloutDependencyPhaseAnnotationKey:  config.RolloutDependencyPhaseSoaking,
				config.RolloutDependencyReasonAnnotationKey: fmt.Sprintf("soaking upstream %q", upstream.Name),
				config.RolloutSoakStartedAtAnnotationKey:    now,
				config.RolloutRestartBaselineAnnotationKey:  baseline,
			},
		},
		"spec": map[string]interface{}{
			"paused": true,
		},
	})
}

func (c *PhasedDeploymentController) resetSoak(ctx context.Context, dep *appsv1.Deployment, reason string) error {
	c.setPhaseMetric(dep.Name, config.RolloutDependencyPhaseWaiting)
	c.soakRemaining.WithLabelValues(dep.Name).Set(0)
	annotations := map[string]interface{}{
		config.RolloutDependencyPhaseAnnotationKey:  config.RolloutDependencyPhaseWaiting,
		config.RolloutDependencyReasonAnnotationKey: reason,
		config.RolloutSoakStartedAtAnnotationKey:    nil,
		config.RolloutRestartBaselineAnnotationKey:  nil,
	}
	return c.patchDeployment(ctx, dep.Name, map[string]interface{}{
		"metadata": map[string]interface{}{
			"annotations": annotations,
		},
		"spec": map[string]interface{}{
			"paused": true,
		},
	})
}

func (c *PhasedDeploymentController) completeGate(ctx context.Context, dep *appsv1.Deployment, reason string) error {
	hadPaused := annotationOrEmpty(dep, config.RolloutHadPausedAnnotationKey) == phased.HadPausedAnnotationTrue
	paused := hadPaused
	c.setPhaseMetric(dep.Name, config.RolloutDependencyPhaseComplete)
	c.blocked.DeleteLabelValues(dep.Name, "restarts")
	c.blocked.DeleteLabelValues(dep.Name, "config")
	c.soakRemaining.WithLabelValues(dep.Name).Set(0)

	annotations := map[string]interface{}{
		config.RolloutDependencyPhaseAnnotationKey:    config.RolloutDependencyPhaseComplete,
		config.RolloutDependencyRevisionAnnotationKey: phased.Revision(dep),
		config.RolloutDependencyReasonAnnotationKey:   reason,
		config.RolloutSoakStartedAtAnnotationKey:      nil,
		config.RolloutRestartBaselineAnnotationKey:    nil,
		config.RolloutResumeAnnotationKey:             nil,
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

func (c *PhasedDeploymentController) ensurePaused(ctx context.Context, dep *appsv1.Deployment) error {
	if dep.Spec.Paused {
		return nil
	}
	return c.patchDeployment(ctx, dep.Name, map[string]interface{}{
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

func (c *PhasedDeploymentController) listDeploymentPods(dep *appsv1.Deployment) ([]*corev1.Pod, error) {
	if dep.Spec.Selector == nil {
		return nil, nil
	}
	selector, err := metav1.LabelSelectorAsSelector(dep.Spec.Selector)
	if err != nil {
		return nil, err
	}
	pods, err := c.podLister.Pods(c.namespace).List(selector)
	if err != nil {
		return nil, err
	}
	out := make([]*corev1.Pod, 0, len(pods))
	for _, p := range pods {
		if p.DeletionTimestamp != nil {
			continue
		}
		out = append(out, p)
	}
	return out, nil
}

func (c *PhasedDeploymentController) setPhaseMetric(name, phase string) {
	for _, p := range []string{
		config.RolloutDependencyPhaseWaiting,
		config.RolloutDependencyPhaseSoaking,
		config.RolloutDependencyPhaseBlocked,
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
	c.restartRatio.DeleteLabelValues(name)
	c.soakRemaining.DeleteLabelValues(name)
}

func annotationOrEmpty(dep *appsv1.Deployment, key string) string {
	if dep.Annotations == nil {
		return ""
	}
	return dep.Annotations[key]
}

func parseSoakStartedAt(dep *appsv1.Deployment) (time.Time, bool) {
	raw := annotationOrEmpty(dep, config.RolloutSoakStartedAtAnnotationKey)
	if raw == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}
