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

// EvaluationResult is the raw outcome of evaluating a single check.
type EvaluationResult string

const (
	ResultPass   EvaluationResult = "pass"
	ResultFail   EvaluationResult = "fail"
	ResultNoData EvaluationResult = "no_data"
	ResultError  EvaluationResult = "error"
)

// CheckOutcome is the action taken after mapping an EvaluationResult through onFailure/onNoData.
type CheckOutcome string

const (
	OutcomePass    CheckOutcome = "pass"
	OutcomePause   CheckOutcome = "pause"
	OutcomeWarn    CheckOutcome = "warn"
	OutcomeSkipped CheckOutcome = "skipped"
)

// EvaluateRequest holds inputs for evaluating a RolloutHealthCheck against a rollout group.
type EvaluateRequest struct {
	Config        *Config
	Namespace     string
	RolloutGroup  string
	CandidatePods []string
	StablePods    []string
	BaselineTime  time.Time
	Now           time.Time
}

// EvaluateResponse is the aggregate result of all checks.
type EvaluateResponse struct {
	Outcome      CheckOutcome
	FailedCheck  string
	Message      string
	CheckResults map[string]EvaluationResult
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

// Evaluate runs all enabled checks in cfg. Returns OutcomePause when any check requires pausing.
func (e *Evaluator) Evaluate(ctx context.Context, req EvaluateRequest) EvaluateResponse {
	resp := EvaluateResponse{
		Outcome:      OutcomePass,
		CheckResults: map[string]EvaluationResult{},
	}
	if req.Config == nil {
		return resp
	}
	if req.Now.IsZero() {
		req.Now = time.Now()
	}

	querier, err := e.factory(req.Config.PrometheusURL)
	if err != nil {
		msg := fmt.Sprintf("failed to create Prometheus client for %q: %v", req.Config.PrometheusURL, err)
		level.Error(e.logger).Log("msg", msg, "rollout_group", req.RolloutGroup)
		resp.Outcome = OutcomePause
		resp.Message = msg
		return resp
	}

	candidateMatchers := buildTargetMatchers(req.Namespace, req.CandidatePods)
	stableMatchers := buildTargetMatchers(req.Namespace, req.StablePods)

	for _, check := range req.Config.Checks {
		if check.Disabled {
			resp.CheckResults[check.Name] = ResultPass
			e.observeEvaluation(req.RolloutGroup, check.Name, "skipped")
			continue
		}

		result, msg := e.evaluateCheck(ctx, querier, check, candidateMatchers, stableMatchers, req)
		resp.CheckResults[check.Name] = result
		e.observeEvaluation(req.RolloutGroup, check.Name, string(result))

		outcome := mapResultToOutcome(result, check)
		if outcome == OutcomeSkipped || outcome == OutcomePass {
			continue
		}
		if outcome == OutcomeWarn {
			if resp.Outcome == OutcomePass {
				resp.Outcome = OutcomeWarn
				resp.FailedCheck = check.Name
				resp.Message = msg
			}
			continue
		}
		// Pause wins over Warn.
		resp.Outcome = OutcomePause
		resp.FailedCheck = check.Name
		resp.Message = msg
		return resp
	}

	return resp
}

func mapResultToOutcome(result EvaluationResult, check Check) CheckOutcome {
	switch result {
	case ResultPass:
		return OutcomePass
	case ResultFail:
		return actionToOutcome(check.OnFailure)
	case ResultNoData:
		return actionToOutcome(check.OnNoData)
	case ResultError:
		// Unreachable / query errors always pause unless the check is fully disabled (handled earlier).
		return actionToOutcome(check.OnFailure)
	default:
		return OutcomePause
	}
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

func (e *Evaluator) evaluateCheck(ctx context.Context, querier Querier, check Check, candidateMatchers, stableMatchers string, req EvaluateRequest) (EvaluationResult, string) {
	currentQuery := substituteQuery(check.Query, candidateMatchers, formatDuration(check.CurrentRange))
	baselineQuery := substituteQuery(check.Query, stableMatchers, formatDuration(check.BaselineRange))

	currentVal, err := e.queryScalar(ctx, querier, currentQuery, req.Now, req.RolloutGroup, check, "current")
	if err != nil {
		return ResultError, fmt.Sprintf("check %q current query failed: %v", check.Name, err)
	}
	if currentVal == nil {
		return ResultNoData, fmt.Sprintf("check %q current query returned no data", check.Name)
	}

	baselineTS := req.BaselineTime
	if baselineTS.IsZero() {
		baselineTS = req.Now
	}
	baselineVal, err := e.queryScalar(ctx, querier, baselineQuery, baselineTS, req.RolloutGroup, check, "baseline")
	if err != nil {
		return ResultError, fmt.Sprintf("check %q baseline query failed: %v", check.Name, err)
	}
	if baselineVal == nil {
		return ResultNoData, fmt.Sprintf("check %q baseline query returned no data", check.Name)
	}

	successQuery := substituteSuccessQuery(check.SuccessQuery, *currentVal, *baselineVal)
	successVal, err := e.queryScalar(ctx, querier, successQuery, req.Now, req.RolloutGroup, check, "success")
	if err != nil {
		return ResultError, fmt.Sprintf("check %q success query failed: %v", check.Name, err)
	}
	if successVal == nil {
		return ResultNoData, fmt.Sprintf("check %q success query returned no data", check.Name)
	}
	if *successVal == 1 {
		return ResultPass, ""
	}
	if *successVal == 0 {
		return ResultFail, fmt.Sprintf("check %q failed (current=%v baseline=%v)", check.Name, *currentVal, *baselineVal)
	}
	return ResultFail, fmt.Sprintf("check %q success query returned unexpected scalar %v (want 0 or 1)", check.Name, *successVal)
}

func (e *Evaluator) queryScalar(ctx context.Context, querier Querier, query string, ts time.Time, rolloutGroup string, check Check, queryType string) (*float64, error) {
	var lastErr error
	attempts := check.QueryRetries + 1
	for attempt := 0; attempt < attempts; attempt++ {
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
			lastErr = err
			continue
		}
		scalar, err := valueToScalar(value)
		if err != nil {
			return nil, err
		}
		return scalar, nil
	}
	return nil, lastErr
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

// buildTargetMatchers returns PromQL label matchers for namespace and pod names.
func buildTargetMatchers(namespace string, podNames []string) string {
	quotedNS := strconv.Quote(namespace)
	if len(podNames) == 0 {
		return fmt.Sprintf(`namespace=%s,pod=~"^$"`, quotedNS)
	}
	parts := make([]string, 0, len(podNames))
	for _, name := range podNames {
		parts = append(parts, regexp.QuoteMeta(name))
	}
	return fmt.Sprintf(`namespace=%s,pod=~"%s"`, quotedNS, strings.Join(parts, "|"))
}
