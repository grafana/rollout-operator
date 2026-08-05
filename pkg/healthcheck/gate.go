package healthcheck

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/go-kit/log"
	"github.com/go-kit/log/level"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
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

type checkProgress struct {
	consecutiveFailures  int
	consecutiveSuccesses int
	errorAttempts        int
	noDataAttempts       int
	lastEvalAt           time.Time
	lastOutcome          CheckOutcome
	lastMessage          string
	lastResult           EvaluationResult
}

// Gate decides whether progression to the next zone should pause for health checks.
type Gate struct {
	provider  ConfigProvider
	evaluator *Evaluator
	metrics   *Metrics
	recorder  record.EventRecorder
	logger    log.Logger

	mu sync.Mutex
	// progress tracks consecutive pass/fail / retry state per rolloutGroup/config/check.
	progress map[string]*checkProgress

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
		progress:  map[string]*checkProgress{},
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
	RolloutGroup string
	// StateKey isolates cached policy progress when workloads share a rollout group.
	// It defaults to RolloutGroup.
	StateKey          string
	Namespace         string
	TargetName        string
	TargetKind        string
	TargetLabels      map[string]string
	TargetAnnotations map[string]string
	EventTarget       runtime.Object
	CandidatePods     []*corev1.Pod
	StablePods        []*corev1.Pod
	BaselineTime      time.Time
	Now               time.Time
}

// Evaluate resolves the RolloutHealthCheck annotation on the target workload and evaluates checks.
// Missing / mismatched bindings proceed (ShouldPause=false) but emit misconfiguration signals.
func (g *Gate) Evaluate(ctx context.Context, req Request) Decision {
	stateKey := requestStateKey(req)
	checkName := strings.TrimSpace(req.TargetAnnotations[config.RolloutHealthCheckAnnotationKey])
	if checkName == "" {
		g.clearGroupProgress(stateKey)
		g.clearMisconfigured(stateKey, req.RolloutGroup)
		g.setBlocked(req.RolloutGroup, false)
		return Decision{}
	}

	cfg := g.provider.Get(checkName)
	if cfg == nil {
		msg := fmt.Sprintf("RolloutHealthCheck %q referenced by annotation %s was not found", checkName, config.RolloutHealthCheckAnnotationKey)
		g.reportMisconfigured(req, msg)
		return Decision{Reason: msg}
	}

	if !cfg.MatchesLabels(labels.Set(req.TargetLabels)) {
		msg := fmt.Sprintf("RolloutHealthCheck %q selector does not match %s %s", checkName, req.TargetKind, req.TargetName)
		g.reportMisconfigured(req, msg)
		return Decision{Reason: msg}
	}

	now := req.Now
	if now.IsZero() {
		now = time.Now()
	}

	// Skip Prometheus queries while every check is still inside its retry/reevaluation window.
	if cached, ok := g.cachedDecision(stateKey, cfg, now); ok {
		return g.finalize(req, cached, false)
	}

	resp := g.evaluator.Evaluate(ctx, EvaluateRequest{
		Config:        cfg,
		Namespace:     req.Namespace,
		RolloutGroup:  req.RolloutGroup,
		CandidatePods: targetPodsFromCore(req.CandidatePods),
		StablePods:    targetPodsFromCore(req.StablePods),
		BaselineTime:  req.BaselineTime,
		Now:           now,
	})

	if resp.ClientError != "" {
		// Client construction failure is not per-check; pause safely.
		return g.finalize(req, Decision{ShouldPause: true, Reason: resp.ClientError}, true)
	}

	decision := g.applyPolicies(stateKey, cfg, resp, now)
	return g.finalize(req, decision, true)
}

func (g *Gate) finalize(req Request, decision Decision, emitEvent bool) Decision {
	if decision.ShouldPause {
		level.Warn(g.logger).Log("msg", "rollout blocked by health check", "rollout_group", req.RolloutGroup, "workload_kind", req.TargetKind, "workload", req.TargetName, "detail", decision.Reason)
		if emitEvent {
			g.event(req.EventTarget, corev1.EventTypeWarning, eventBlocked, decision.Reason)
		}
		g.clearMisconfigured(requestStateKey(req), req.RolloutGroup)
		g.setBlocked(req.RolloutGroup, true)
		return decision
	}
	if decision.Reason != "" {
		level.Warn(g.logger).Log("msg", "health check warning", "rollout_group", req.RolloutGroup, "workload_kind", req.TargetKind, "workload", req.TargetName, "detail", decision.Reason)
		if emitEvent {
			g.event(req.EventTarget, corev1.EventTypeWarning, eventWarn, decision.Reason)
		}
	}
	g.clearMisconfigured(requestStateKey(req), req.RolloutGroup)
	g.setBlocked(req.RolloutGroup, false)
	return decision
}

func (g *Gate) applyPolicies(rolloutGroup string, cfg *Config, resp EvaluateResponse, now time.Time) Decision {
	checkByName := map[string]Check{}
	for _, c := range cfg.Checks {
		checkByName[c.Name] = c
	}

	var (
		shouldPause bool
		warnReason  string
		pauseReason string
	)

	for _, ev := range resp.Checks {
		check, ok := checkByName[ev.Name]
		if !ok {
			continue
		}
		outcome, msg := g.applyCheckPolicy(rolloutGroup, cfg.Name, check, ev, now)
		switch outcome {
		case OutcomePause:
			shouldPause = true
			if pauseReason == "" {
				pauseReason = msg
			}
		case OutcomeWarn:
			if warnReason == "" {
				warnReason = msg
			}
		}
	}

	if shouldPause {
		return Decision{ShouldPause: true, Reason: pauseReason}
	}
	if warnReason != "" {
		return Decision{Reason: warnReason}
	}
	return Decision{}
}

func (g *Gate) applyCheckPolicy(rolloutGroup, configName string, check Check, ev CheckEvaluation, now time.Time) (CheckOutcome, string) {
	key := progressKey(rolloutGroup, configName, check.Name)

	switch ev.Result {
	case ResultSkipped:
		g.clearProgress(key)
		return OutcomeSkipped, ""
	case ResultError:
		return g.applyRetryPolicy(key, check.ErrorPolicy, ResultError, ev.Message, now, "error")
	case ResultNoData:
		return g.applyRetryPolicy(key, check.NoDataPolicy, ResultNoData, ev.Message, now, "no-data")
	case ResultPass, ResultFail:
		return g.applyFailurePolicy(key, check, ev, now)
	default:
		return OutcomePause, ev.Message
	}
}

func (g *Gate) applyRetryPolicy(key string, policy RetryPolicy, result EvaluationResult, message string, now time.Time, kind string) (CheckOutcome, string) {
	g.mu.Lock()
	defer g.mu.Unlock()

	prog := g.progress[key]
	if prog == nil {
		prog = &checkProgress{}
		g.progress[key] = prog
	}

	attempts := &prog.errorAttempts
	if result == ResultNoData {
		attempts = &prog.noDataAttempts
	}
	*attempts++

	prog.lastEvalAt = now
	prog.lastResult = result
	msg := message
	if msg == "" {
		msg = fmt.Sprintf("%s on health check", kind)
	}

	if *attempts < policy.MaxAttempts {
		prog.lastOutcome = OutcomePause
		prog.lastMessage = fmt.Sprintf("%s (attempt %d/%d)", msg, *attempts, policy.MaxAttempts)
		return OutcomePause, prog.lastMessage
	}

	outcome := actionToOutcome(policy.ExhaustedAction)
	prog.lastOutcome = outcome
	prog.lastMessage = fmt.Sprintf("%s after %d attempts", msg, *attempts)
	return outcome, prog.lastMessage
}

func (g *Gate) applyFailurePolicy(key string, check Check, ev CheckEvaluation, now time.Time) (CheckOutcome, string) {
	g.mu.Lock()
	defer g.mu.Unlock()

	prog := g.progress[key]
	if prog == nil {
		prog = &checkProgress{}
		g.progress[key] = prog
	}

	// Fresh query results always update counters. reevaluationInterval only gates whether
	// Evaluate re-queries (via cachedDecision), not whether new results are applied.
	prog.lastEvalAt = now
	prog.lastResult = ev.Result
	prog.errorAttempts = 0
	prog.noDataAttempts = 0
	msg := ev.Message

	if ev.Result == ResultPass {
		prog.consecutiveSuccesses++
		prog.consecutiveFailures = 0
		if prog.consecutiveSuccesses >= check.FailurePolicy.ConsecutiveSuccesses {
			prog.lastOutcome = OutcomePass
			prog.lastMessage = ""
			return OutcomePass, ""
		}
		prog.lastOutcome = OutcomePause
		prog.lastMessage = fmt.Sprintf("check %q needs %d consecutive successes (have %d)", check.Name, check.FailurePolicy.ConsecutiveSuccesses, prog.consecutiveSuccesses)
		return OutcomePause, prog.lastMessage
	}

	prog.consecutiveFailures++
	prog.consecutiveSuccesses = 0
	if msg == "" {
		msg = fmt.Sprintf("check %q failed", check.Name)
	}
	if prog.consecutiveFailures >= check.FailurePolicy.ConsecutiveFailures {
		outcome := actionToOutcome(check.FailurePolicy.ExceededAction)
		prog.lastOutcome = outcome
		prog.lastMessage = msg
		return outcome, msg
	}
	prog.lastOutcome = OutcomePause
	prog.lastMessage = fmt.Sprintf("%s (%d/%d consecutive failures)", msg, prog.consecutiveFailures, check.FailurePolicy.ConsecutiveFailures)
	return OutcomePause, prog.lastMessage
}

func (g *Gate) cachedDecision(rolloutGroup string, cfg *Config, now time.Time) (Decision, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()

	var (
		sawAny      bool
		shouldPause bool
		warnReason  string
		pauseReason string
	)
	for _, check := range cfg.Checks {
		if check.Disabled {
			continue
		}
		prog := g.progress[progressKey(rolloutGroup, cfg.Name, check.Name)]
		if prog == nil || prog.lastEvalAt.IsZero() {
			return Decision{}, false
		}
		interval := holdInterval(check, prog.lastResult)
		if !now.Before(prog.lastEvalAt.Add(interval)) {
			return Decision{}, false
		}
		sawAny = true
		switch prog.lastOutcome {
		case OutcomePause:
			shouldPause = true
			if pauseReason == "" {
				pauseReason = prog.lastMessage
			}
		case OutcomeWarn:
			if warnReason == "" {
				warnReason = prog.lastMessage
			}
		}
	}
	if !sawAny {
		return Decision{}, false
	}
	if shouldPause {
		return Decision{ShouldPause: true, Reason: pauseReason}, true
	}
	if warnReason != "" {
		return Decision{Reason: warnReason}, true
	}
	return Decision{}, true
}

func holdInterval(check Check, last EvaluationResult) time.Duration {
	switch last {
	case ResultError:
		return check.ErrorPolicy.RetryInterval
	case ResultNoData:
		return check.NoDataPolicy.RetryInterval
	default:
		return check.FailurePolicy.ReevaluationInterval
	}
}

func (g *Gate) clearProgress(key string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.progress, key)
}

func (g *Gate) clearGroupProgress(rolloutGroup string) {
	prefix := rolloutGroup + "/"
	g.mu.Lock()
	defer g.mu.Unlock()
	for k := range g.progress {
		if strings.HasPrefix(k, prefix) {
			delete(g.progress, k)
		}
	}
}

func progressKey(rolloutGroup, configName, checkName string) string {
	return rolloutGroup + "/" + configName + "/" + checkName
}

func (g *Gate) reportMisconfigured(req Request, msg string) {
	level.Error(g.logger).Log("msg", "rollout health check misconfigured", "rollout_group", req.RolloutGroup, "workload_kind", req.TargetKind, "workload", req.TargetName, "detail", msg)
	_, already := g.misconfiguredGroups.LoadOrStore(requestStateKey(req), struct{}{})
	if !already {
		g.event(req.EventTarget, corev1.EventTypeWarning, eventMisconfigured, msg)
		if g.metrics != nil {
			g.metrics.MisconfiguredTotal.WithLabelValues(req.RolloutGroup).Inc()
		}
	}
	if g.metrics != nil {
		g.metrics.Misconfigured.WithLabelValues(req.RolloutGroup).Set(1)
	}
	g.setBlocked(req.RolloutGroup, false)
}

func (g *Gate) clearMisconfigured(stateKey, rolloutGroup string) {
	if _, loaded := g.misconfiguredGroups.LoadAndDelete(stateKey); loaded || g.metrics != nil {
		if g.metrics != nil {
			g.metrics.Misconfigured.WithLabelValues(rolloutGroup).Set(0)
		}
	}
}

func requestStateKey(req Request) string {
	if req.StateKey != "" {
		return req.StateKey
	}
	return req.RolloutGroup
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

func (g *Gate) event(target runtime.Object, eventType, reason, message string) {
	if g.recorder == nil || target == nil {
		return
	}
	g.recorder.Event(target, eventType, reason, message)
}

func targetPodsFromCore(pods []*corev1.Pod) TargetPods {
	names := make([]string, 0, len(pods))
	zones := make([]string, 0, len(pods))
	for _, p := range pods {
		names = append(names, p.Name)
		if z := p.Labels["name"]; z != "" {
			zones = append(zones, z)
		}
	}
	return TargetPods{Names: names, Zones: uniqueNonEmpty(zones)}
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
