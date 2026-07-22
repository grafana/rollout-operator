package healthcheck

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-kit/log"
	v1 "github.com/prometheus/client_golang/api/prometheus/v1"
	"github.com/prometheus/common/model"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/labels"
)

type fakeQuerier struct {
	mu       sync.Mutex
	calls    []string
	sequence []func() (model.Value, error)
	seqIdx   int
}

func (f *fakeQuerier) Query(_ context.Context, query string, _ time.Time) (model.Value, v1.Warnings, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, query)
	if f.seqIdx >= len(f.sequence) {
		return &model.Scalar{Value: 0}, nil, nil
	}
	fn := f.sequence[f.seqIdx]
	f.seqIdx++
	val, err := fn()
	return val, nil, err
}

func scalar(v float64) model.Value {
	return &model.Scalar{Value: model.SampleValue(v), Timestamp: model.Now()}
}

func TestEvaluator_PassFailNoDataError(t *testing.T) {
	baseCfg := &Config{
		Name:          "hc",
		PrometheusURL: "http://prometheus",
		Selector:      labels.SelectorFromSet(labels.Set{"rollout-group": "ingester"}),
		Checks: []Check{{
			Name:          "errors",
			CurrentRange:  time.Minute,
			BaselineRange: 2 * time.Minute,
			QueryTimeout:  time.Second,
			QueryRetries:  0,
			OnFailure:     ActionPause,
			OnNoData:      ActionPause,
			Query:         `scalar(sum(rate(errors{${targetMatchers}}[${range}])))`,
			SuccessQuery:  `(${current} < bool 1) or (${current} < bool (2 * ${baseline}))`,
		}},
	}

	t.Run("pass", func(t *testing.T) {
		q := &fakeQuerier{sequence: []func() (model.Value, error){
			func() (model.Value, error) { return scalar(0.1), nil },
			func() (model.Value, error) { return scalar(0.2), nil },
			func() (model.Value, error) { return scalar(1), nil },
		}}
		e := NewEvaluator(func(string) (Querier, error) { return q, nil }, nil, log.NewNopLogger())
		resp := e.Evaluate(context.Background(), EvaluateRequest{
			Config:        baseCfg,
			Namespace:     "ns",
			RolloutGroup:  "ingester",
			CandidatePods: []string{"ingester-zone-a-0"},
			StablePods:    []string{"ingester-zone-b-0"},
			Now:           time.Now(),
			BaselineTime:  time.Now().Add(-time.Hour),
		})
		require.Equal(t, OutcomePass, resp.Outcome)
		require.Equal(t, ResultPass, resp.CheckResults["errors"])
		require.Len(t, q.calls, 3)
		require.True(t, strings.Contains(q.calls[0], `pod=~"ingester-zone-a-0"`))
		require.True(t, strings.Contains(q.calls[0], `[1m]`))
		require.True(t, strings.Contains(q.calls[1], `pod=~"ingester-zone-b-0"`))
		require.True(t, strings.Contains(q.calls[1], `[2m]`))
		require.True(t, strings.Contains(q.calls[2], "0.1"))
		require.True(t, strings.Contains(q.calls[2], "0.2"))
	})

	t.Run("fail pauses", func(t *testing.T) {
		q := &fakeQuerier{sequence: []func() (model.Value, error){
			func() (model.Value, error) { return scalar(5), nil },
			func() (model.Value, error) { return scalar(0.1), nil },
			func() (model.Value, error) { return scalar(0), nil },
		}}
		e := NewEvaluator(func(string) (Querier, error) { return q, nil }, nil, log.NewNopLogger())
		resp := e.Evaluate(context.Background(), EvaluateRequest{Config: baseCfg, Namespace: "ns", RolloutGroup: "ingester", Now: time.Now()})
		require.Equal(t, OutcomePause, resp.Outcome)
		require.Equal(t, ResultFail, resp.CheckResults["errors"])
	})

	t.Run("no data pauses", func(t *testing.T) {
		q := &fakeQuerier{sequence: []func() (model.Value, error){
			func() (model.Value, error) { return model.Vector{}, nil },
		}}
		e := NewEvaluator(func(string) (Querier, error) { return q, nil }, nil, log.NewNopLogger())
		resp := e.Evaluate(context.Background(), EvaluateRequest{Config: baseCfg, Namespace: "ns", RolloutGroup: "ingester", Now: time.Now()})
		require.Equal(t, OutcomePause, resp.Outcome)
		require.Equal(t, ResultNoData, resp.CheckResults["errors"])
	})

	t.Run("error pauses", func(t *testing.T) {
		q := &fakeQuerier{sequence: []func() (model.Value, error){
			func() (model.Value, error) { return nil, errors.New("boom") },
		}}
		e := NewEvaluator(func(string) (Querier, error) { return q, nil }, nil, log.NewNopLogger())
		resp := e.Evaluate(context.Background(), EvaluateRequest{Config: baseCfg, Namespace: "ns", RolloutGroup: "ingester", Now: time.Now()})
		require.Equal(t, OutcomePause, resp.Outcome)
		require.Equal(t, ResultError, resp.CheckResults["errors"])
	})

	t.Run("warn on failure", func(t *testing.T) {
		cfg := *baseCfg
		cfg.Checks = append([]Check(nil), baseCfg.Checks...)
		cfg.Checks[0].OnFailure = ActionWarn
		q := &fakeQuerier{sequence: []func() (model.Value, error){
			func() (model.Value, error) { return scalar(5), nil },
			func() (model.Value, error) { return scalar(0.1), nil },
			func() (model.Value, error) { return scalar(0), nil },
		}}
		e := NewEvaluator(func(string) (Querier, error) { return q, nil }, nil, log.NewNopLogger())
		resp := e.Evaluate(context.Background(), EvaluateRequest{Config: &cfg, Namespace: "ns", RolloutGroup: "ingester", Now: time.Now()})
		require.Equal(t, OutcomeWarn, resp.Outcome)
	})

	t.Run("retries then succeeds", func(t *testing.T) {
		cfg := *baseCfg
		cfg.Checks = append([]Check(nil), baseCfg.Checks...)
		cfg.Checks[0].QueryRetries = 2
		attempts := 0
		q := &fakeQuerier{sequence: []func() (model.Value, error){
			func() (model.Value, error) {
				attempts++
				return nil, errors.New("transient")
			},
			func() (model.Value, error) {
				attempts++
				return scalar(0.1), nil
			},
			func() (model.Value, error) { return scalar(0.2), nil },
			func() (model.Value, error) { return scalar(1), nil },
		}}
		e := NewEvaluator(func(string) (Querier, error) { return q, nil }, nil, log.NewNopLogger())
		resp := e.Evaluate(context.Background(), EvaluateRequest{Config: &cfg, Namespace: "ns", RolloutGroup: "ingester", Now: time.Now()})
		require.Equal(t, OutcomePass, resp.Outcome)
		require.Equal(t, 2, attempts)
	})

	t.Run("disabled check skipped", func(t *testing.T) {
		cfg := *baseCfg
		cfg.Checks = append([]Check(nil), baseCfg.Checks...)
		cfg.Checks[0].Disabled = true
		q := &fakeQuerier{}
		e := NewEvaluator(func(string) (Querier, error) { return q, nil }, nil, log.NewNopLogger())
		resp := e.Evaluate(context.Background(), EvaluateRequest{Config: &cfg, Namespace: "ns", RolloutGroup: "ingester", Now: time.Now()})
		require.Equal(t, OutcomePass, resp.Outcome)
		require.Empty(t, q.calls)
	})
}

func TestBuildTargetMatchers(t *testing.T) {
	require.Equal(t, `namespace="ns",pod=~"^$"`, buildTargetMatchers("ns", nil))
	require.Equal(t, `namespace="ns",pod=~"a-0|b-1"`, buildTargetMatchers("ns", []string{"a-0", "b-1"}))
}
