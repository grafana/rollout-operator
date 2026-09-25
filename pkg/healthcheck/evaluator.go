package healthcheck

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-kit/log"
	"github.com/go-kit/log/level"
	"github.com/prometheus/client_golang/api"
	v1 "github.com/prometheus/client_golang/api/prometheus/v1"
	"github.com/prometheus/common/model"
)

// Querier abstracts the Prometheus instant-query API for tests.
type Querier interface {
	Query(ctx context.Context, query string, ts time.Time) (model.Value, v1.Warnings, error)
}

type prometheusQuerier struct {
	api v1.API
}

func (q *prometheusQuerier) Query(ctx context.Context, query string, ts time.Time) (model.Value, v1.Warnings, error) {
	return q.api.Query(ctx, query, ts)
}

// NewPrometheusQuerier builds a Querier against the given Prometheus base URL.
func NewPrometheusQuerier(prometheusURL string) (Querier, error) {
	client, err := api.NewClient(api.Config{Address: prometheusURL})
	if err != nil {
		return nil, err
	}
	return &prometheusQuerier{api: v1.NewAPI(client)}, nil
}

// QuerierFactory creates a Querier for a Prometheus URL. Overridable in tests.
type QuerierFactory func(prometheusURL string) (Querier, error)

// EvaluationResult is the raw outcome of evaluating a single check before policy mapping.
type EvaluationResult string

const (
	ResultPass    EvaluationResult = "pass"
	ResultFail    EvaluationResult = "fail"
	ResultNoData  EvaluationResult = "no_data"
	ResultError   EvaluationResult = "error"
	ResultSkipped EvaluationResult = "skipped"
)

// CheckOutcome is the action taken after mapping an EvaluationResult through policies.
type CheckOutcome string

const (
	OutcomePass    CheckOutcome = "pass"
	OutcomePause   CheckOutcome = "pause"
	OutcomeWarn    CheckOutcome = "warn"
	OutcomeSkipped CheckOutcome = "skipped"
)

// TargetPods describes pods for ${targetMatchers} substitution.
type TargetPods struct {
	Names []string
	// Zones are StatefulSet / zone identifiers (typically the pod "name" label).
	Zones []string
}

// EvaluateRequest holds inputs for evaluating a RolloutHealthCheck against a rollout group.
type EvaluateRequest struct {
	Config        *Config
	Namespace     string
	RolloutGroup  string
	CandidatePods TargetPods
	StablePods    TargetPods
	BaselineTime  time.Time
	Now           time.Time
}

// CheckEvaluation is the raw result of one check.
type CheckEvaluation struct {
	Name          string
	Result        EvaluationResult
	Message       string
	CurrentValue  *float64
	BaselineValue *float64
	QueryType     string
}

// EvaluateResponse is the aggregate raw result of all checks.
type EvaluateResponse struct {
	Checks  []CheckEvaluation
	Results map[string]EvaluationResult
	// ClientError is set when the Prometheus client could not be created.
	ClientError string
}

// Evaluator runs PromQL health checks.
type Evaluator struct {
	factory QuerierFactory
	metrics *Metrics
	logger  log.Logger
}

// NewEvaluator creates an Evaluator. factory may be nil to use NewPrometheusQuerier.
func NewEvaluator(factory QuerierFactory, metrics *Metrics, logger log.Logger) *Evaluator {
	if factory == nil {
		factory = NewPrometheusQuerier
	}
	return &Evaluator{factory: factory, metrics: metrics, logger: logger}
}

// Evaluate runs all enabled checks and returns raw results. Policy mapping and retries across
// reconciles are handled by Gate so evaluation does not block the controller on sleep.
func (e *Evaluator) Evaluate(ctx context.Context, req EvaluateRequest) EvaluateResponse {
	resp := EvaluateResponse{
		Results: map[string]EvaluationResult{},
	}
	if req.Config == nil {
		return resp
	}
	if req.Now.IsZero() {
		req.Now = time.Now()
	}
	level.Info(e.logger).Log(
		"msg", "health check evaluation started",
		"rollout_group", req.RolloutGroup,
		"policy", req.Config.Name,
		"candidate_pods", len(req.CandidatePods.Names),
		"stable_pods", len(req.StablePods.Names),
		"checks", len(req.Config.Checks),
	)
	querier, err := e.factory(req.Config.PrometheusURL)
	if err != nil {
		msg := fmt.Sprintf("failed to create Prometheus client: %v", err)
		level.Error(e.logger).Log(
			"msg", "prometheus client unavailable",
			"rollout_group", req.RolloutGroup,
			"policy", req.Config.Name,
			"err", err,
		)
		resp.ClientError = msg
		return resp
	}

	candidateMatchers := buildTargetMatchers(req.Namespace, req.CandidatePods)
	stableMatchers := buildTargetMatchers(req.Namespace, req.StablePods)

	for _, check := range req.Config.Checks {
		if check.Disabled {
			ev := CheckEvaluation{Name: check.Name, Result: ResultSkipped}
			resp.Checks = append(resp.Checks, ev)
			resp.Results[check.Name] = ResultSkipped
			e.observeEvaluation(req.RolloutGroup, check.Name, string(ResultSkipped))
			continue
		}

		ev := e.evaluateCheck(ctx, querier, check, candidateMatchers, stableMatchers, req)
		resp.Checks = append(resp.Checks, ev)
		resp.Results[check.Name] = ev.Result
		e.observeEvaluation(req.RolloutGroup, check.Name, string(ev.Result))
	}

	return resp
}

func (e *Evaluator) evaluateCheck(ctx context.Context, querier Querier, check Check, candidateMatchers, stableMatchers string, req EvaluateRequest) CheckEvaluation {
	ev := CheckEvaluation{Name: check.Name}
	currentQuery := substituteQuery(check.Query, candidateMatchers, formatDuration(check.CurrentRange))
	baselineQuery := substituteQuery(check.Query, stableMatchers, formatDuration(check.BaselineRange))

	currentVal, result, errMsg := e.queryScalarOnce(ctx, querier, currentQuery, req.Now, req.RolloutGroup, check, "current")
	if result != ResultPass {
		ev.Result = result
		ev.Message = fmt.Sprintf("check %q current query: %s", check.Name, errMsg)
		ev.QueryType = "current"
		return ev
	}
	ev.CurrentValue = currentVal

	baselineTS := req.BaselineTime
	if baselineTS.IsZero() {
		baselineTS = req.Now
	}
	baselineVal, result, errMsg := e.queryScalarOnce(ctx, querier, baselineQuery, baselineTS, req.RolloutGroup, check, "baseline")
	if result != ResultPass {
		ev.Result = result
		ev.Message = fmt.Sprintf("check %q baseline query: %s", check.Name, errMsg)
		ev.QueryType = "baseline"
		return ev
	}
	ev.BaselineValue = baselineVal

	successQuery := substituteSuccessQuery(check.SuccessQuery, *currentVal, *baselineVal)
	successVal, result, errMsg := e.queryScalarOnce(ctx, querier, successQuery, req.Now, req.RolloutGroup, check, "success")
	if result != ResultPass {
		ev.Result = result
		ev.Message = fmt.Sprintf("check %q success query: %s", check.Name, errMsg)
		ev.QueryType = "success"
		return ev
	}
	if *successVal == 1 {
		ev.Result = ResultPass
		return ev
	}
	if *successVal == 0 {
		ev.Result = ResultFail
		ev.Message = fmt.Sprintf("check %q failed (current=%v baseline=%v)", check.Name, *currentVal, *baselineVal)
		return ev
	}
	ev.Result = ResultFail
	ev.Message = fmt.Sprintf("check %q success query returned unexpected scalar %v (want 0 or 1)", check.Name, *successVal)
	return ev
}

// queryScalarOnce performs a single Prometheus query. Retries are applied by Gate across reconciles
// using errorPolicy / noDataPolicy so the controller reconcile loop is not blocked on sleep.
func (e *Evaluator) queryScalarOnce(ctx context.Context, querier Querier, query string, ts time.Time, rolloutGroup string, check Check, queryType string) (*float64, EvaluationResult, string) {
	qctx, cancel := context.WithTimeout(ctx, check.QueryTimeout)
	start := time.Now()
	value, warnings, err := querier.Query(qctx, query, ts)
	cancel()
	if e.metrics != nil {
		e.metrics.QueryDuration.WithLabelValues(rolloutGroup, check.Name, queryType).Observe(time.Since(start).Seconds())
	}
	if len(warnings) > 0 {
		level.Warn(e.logger).Log("msg", "prometheus query warnings", "check", check.Name, "query_type", queryType, "warnings", strings.Join(warnings, "; "))
	}
	if err != nil {
		if ctx.Err() != nil {
			return nil, ResultError, fmt.Sprintf("interrupted: %v", err)
		}
		level.Warn(e.logger).Log(
			"msg", "prometheus query failed",
			"rollout_group", rolloutGroup,
			"check", check.Name,
			"query_type", queryType,
			"err", err,
		)
		return nil, ResultError, err.Error()
	}

	scalar, err := valueToScalar(value)
	if err != nil {
		return nil, ResultError, err.Error()
	}
	if scalar == nil {
		return nil, ResultNoData, "no data"
	}
	return scalar, ResultPass, ""
}

func (e *Evaluator) observeEvaluation(rolloutGroup, check, result string) {
	if e.metrics == nil {
		return
	}
	e.metrics.EvaluationsTotal.WithLabelValues(rolloutGroup, check, result).Inc()
}

func valueToScalar(value model.Value) (*float64, error) {
	if value == nil {
		return nil, nil
	}
	switch v := value.(type) {
	case *model.Scalar:
		if v == nil {
			return nil, nil
		}
		f := float64(v.Value)
		return &f, nil
	case model.Vector:
		if len(v) == 0 {
			return nil, nil
		}
		if len(v) != 1 {
			return nil, fmt.Errorf("expected scalar or single-sample vector, got vector of length %d", len(v))
		}
		f := float64(v[0].Value)
		return &f, nil
	default:
		return nil, fmt.Errorf("expected scalar result, got %s", value.Type())
	}
}

func substituteQuery(query, targetMatchers, rangeStr string) string {
	out := strings.ReplaceAll(query, placeholderTargetMatchers, targetMatchers)
	out = strings.ReplaceAll(out, placeholderRange, rangeStr)
	return out
}

func substituteSuccessQuery(query string, current, baseline float64) string {
	out := strings.ReplaceAll(query, placeholderCurrent, formatFloat(current))
	out = strings.ReplaceAll(out, placeholderBaseline, formatFloat(baseline))
	return out
}

func formatFloat(f float64) string {
	return strconv.FormatFloat(f, 'g', -1, 64)
}

func formatDuration(d time.Duration) string {
	// Prefer compact Prometheus-style durations for common values.
	if d%time.Hour == 0 && d >= time.Hour {
		return fmt.Sprintf("%dh", int(d/time.Hour))
	}
	if d%time.Minute == 0 && d >= time.Minute {
		return fmt.Sprintf("%dm", int(d/time.Minute))
	}
	if d%time.Second == 0 && d >= time.Second {
		return fmt.Sprintf("%ds", int(d/time.Second))
	}
	return d.String()
}

// buildTargetMatchers returns matchers common to application and kube-state-metrics
// series. Pod names already identify the candidate/stable workloads; adding the
// application-specific "name" label would make kube-state-metrics checks return no data.
func buildTargetMatchers(namespace string, targets TargetPods) string {
	quotedNS := strconv.Quote(namespace)
	parts := []string{fmt.Sprintf("namespace=%s", quotedNS)}
	if len(targets.Names) == 0 {
		parts = append(parts, `pod=~"^$"`)
	} else {
		parts = append(parts, fmt.Sprintf(`pod=~"%s"`, joinRegex(targets.Names)))
	}
	return strings.Join(parts, ",")
}

func joinRegex(values []string) string {
	parts := make([]string, 0, len(values))
	for _, v := range values {
		parts = append(parts, regexp.QuoteMeta(v))
	}
	return strings.Join(parts, "|")
}

func uniqueNonEmpty(values []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, v := range values {
		if v == "" {
			continue
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}

func actionToOutcome(action FailureAction) CheckOutcome {
	switch action {
	case ActionWarn:
		return OutcomeWarn
	case ActionDisabled:
		return OutcomeSkipped
	default:
		return OutcomePause
	}
}
