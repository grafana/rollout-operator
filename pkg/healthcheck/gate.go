package healthcheck

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/go-kit/log"
	"github.com/go-kit/log/level"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/tools/record"

	"github.com/grafana/rollout-operator/pkg/config"
)

const (
	eventBlocked       = "RolloutBlockedByHealthCheck"
	eventMisconfigured = "RolloutHealthCheckMisconfigured"
	eventWarn          = "RolloutHealthCheckWarn"
)

// ConfigProvider looks up a RolloutHealthCheck by name.
type ConfigProvider interface {
	Get(name string) *Config
}

// Gate decides whether progression to the next zone should pause for health checks.
type Gate struct {
	provider  ConfigProvider
	evaluator *Evaluator
	metrics   *Metrics
	recorder  record.EventRecorder
	logger    log.Logger

	// Tracks rollout groups currently reported as misconfigured so the counter and
	// events are not re-emitted on every reconcile while the binding stays broken.
	misconfiguredGroups sync.Map
}

// NewGate creates a health-check gate.
func NewGate(provider ConfigProvider, evaluator *Evaluator, metrics *Metrics, recorder record.EventRecorder, logger log.Logger) *Gate {
	return &Gate{
		provider:  provider,
		evaluator: evaluator,
		metrics:   metrics,
		recorder:  recorder,
		logger:    logger,
	}
}

// Decision is the gate result for a single between-zone transition.
type Decision struct {
	// ShouldPause is true when the next zone must not start rolling yet.
	ShouldPause bool
	// Reason is a human-readable explanation when ShouldPause or a warning/misconfiguration occurred.
	Reason string
}

// Request is the input for a between-zone gate evaluation.
type Request struct {
	RolloutGroup  string
	Namespace     string
	Sets          []*appsv1.StatefulSet
	NextSTS       *appsv1.StatefulSet
	CandidatePods []*corev1.Pod
	StablePods    []*corev1.Pod
	BaselineTime  time.Time
}

// Evaluate resolves the RolloutHealthCheck annotation on NextSTS and evaluates checks.
// Missing / mismatched bindings proceed (ShouldPause=false) but emit misconfiguration signals.
func (g *Gate) Evaluate(ctx context.Context, req Request) Decision {
	checkName := strings.TrimSpace(req.NextSTS.Annotations[config.RolloutHealthCheckAnnotationKey])
	if checkName == "" {
		g.setBlocked(req.RolloutGroup, false)
		return Decision{}
	}

	cfg := g.provider.Get(checkName)
	if cfg == nil {
		msg := fmt.Sprintf("RolloutHealthCheck %q referenced by annotation %s was not found", checkName, config.RolloutHealthCheckAnnotationKey)
		g.reportMisconfigured(req, msg)
		return Decision{Reason: msg}
	}

	if !cfg.MatchesLabels(labels.Set(req.NextSTS.Labels)) {
		msg := fmt.Sprintf("RolloutHealthCheck %q selector does not match StatefulSet %s", checkName, req.NextSTS.Name)
		g.reportMisconfigured(req, msg)
		return Decision{Reason: msg}
	}

	resp := g.evaluator.Evaluate(ctx, EvaluateRequest{
		Config:        cfg,
		Namespace:     req.Namespace,
		RolloutGroup:  req.RolloutGroup,
		CandidatePods: podNames(req.CandidatePods),
		StablePods:    podNames(req.StablePods),
		BaselineTime:  req.BaselineTime,
		Now:           time.Now(),
	})

	switch resp.Outcome {
	case OutcomePause:
		msg := resp.Message
		if msg == "" {
			msg = fmt.Sprintf("health check %q paused rollout of group %s", resp.FailedCheck, req.RolloutGroup)
		}
		level.Warn(g.logger).Log("msg", "rollout blocked by health check", "rollout_group", req.RolloutGroup, "statefulset", req.NextSTS.Name, "check", resp.FailedCheck, "detail", msg)
		g.event(req.NextSTS, corev1.EventTypeWarning, eventBlocked, msg)
		g.clearMisconfigured(req.RolloutGroup)
		g.setBlocked(req.RolloutGroup, true)
		return Decision{ShouldPause: true, Reason: msg}
	case OutcomeWarn:
		msg := resp.Message
		if msg == "" {
			msg = fmt.Sprintf("health check %q warned for group %s", resp.FailedCheck, req.RolloutGroup)
		}
		level.Warn(g.logger).Log("msg", "health check warning", "rollout_group", req.RolloutGroup, "statefulset", req.NextSTS.Name, "check", resp.FailedCheck, "detail", msg)
		g.event(req.NextSTS, corev1.EventTypeWarning, eventWarn, msg)
		g.clearMisconfigured(req.RolloutGroup)
		g.setBlocked(req.RolloutGroup, false)
		return Decision{Reason: msg}
	default:
		g.clearMisconfigured(req.RolloutGroup)
		g.setBlocked(req.RolloutGroup, false)
		return Decision{}
	}
}

func (g *Gate) reportMisconfigured(req Request, msg string) {
	level.Error(g.logger).Log("msg", "rollout health check misconfigured", "rollout_group", req.RolloutGroup, "statefulset", req.NextSTS.Name, "detail", msg)
	_, already := g.misconfiguredGroups.LoadOrStore(req.RolloutGroup, struct{}{})
	if !already {
		g.event(req.NextSTS, corev1.EventTypeWarning, eventMisconfigured, msg)
		if g.metrics != nil {
			g.metrics.MisconfiguredTotal.WithLabelValues(req.RolloutGroup).Inc()
		}
	}
	if g.metrics != nil {
		g.metrics.Misconfigured.WithLabelValues(req.RolloutGroup).Set(1)
	}
	g.setBlocked(req.RolloutGroup, false)
}

func (g *Gate) clearMisconfigured(rolloutGroup string) {
	if _, loaded := g.misconfiguredGroups.LoadAndDelete(rolloutGroup); loaded || g.metrics != nil {
		if g.metrics != nil {
			g.metrics.Misconfigured.WithLabelValues(rolloutGroup).Set(0)
		}
	}
}

func (g *Gate) setBlocked(rolloutGroup string, blocked bool) {
	if g.metrics == nil {
		return
	}
	val := 0.0
	if blocked {
		val = 1
	}
	g.metrics.Blocked.WithLabelValues(rolloutGroup).Set(val)
}

func (g *Gate) event(sts *appsv1.StatefulSet, eventType, reason, message string) {
	if g.recorder == nil || sts == nil {
		return
	}
	g.recorder.Event(sts, eventType, reason, message)
}

func podNames(pods []*corev1.Pod) []string {
	names := make([]string, 0, len(pods))
	for _, p := range pods {
		names = append(names, p.Name)
	}
	return names
}

// ParseStartedAtAnnotation parses "<updateRevision>=<RFC3339>" from the annotation value.
// Returns zero time if missing or mismatched revision.
func ParseStartedAtAnnotation(value, updateRevision string) time.Time {
	if value == "" || updateRevision == "" {
		return time.Time{}
	}
	parts := strings.SplitN(value, "=", 2)
	if len(parts) != 2 || parts[0] != updateRevision {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, parts[1])
	if err != nil {
		return time.Time{}
	}
	return t
}

// FormatStartedAtAnnotation builds the annotation value for a revision start time.
func FormatStartedAtAnnotation(updateRevision string, t time.Time) string {
	return updateRevision + "=" + t.UTC().Format(time.RFC3339)
}
