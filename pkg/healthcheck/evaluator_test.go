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

func testCheck() Check {
	return Check{
		Name:          "errors",
		CurrentRange:  time.Minute,
		BaselineRange: 2 * time.Minute,
		QueryTimeout:  time.Second,
		ErrorPolicy: RetryPolicy{
			RetryInterval:   0,
			MaxAttempts:     1,
			ExhaustedAction: ActionPause,
		},
		NoDataPolicy: RetryPolicy{
			RetryInterval:   0,
			MaxAttempts:     1,
			ExhaustedAction: ActionPause,
		},
		FailurePolicy: FailurePolicy{
			ReevaluationInterval: time.Millisecond,
			ConsecutiveFailures:  1,
			ConsecutiveSuccesses: 1,
			ExceededAction:       ActionPause,
		},
		Query:        `scalar(sum(rate(errors{${targetMatchers}}[${range}])))`,
		SuccessQuery: `(${current} < bool 1) or (${current} < bool (2 * ${baseline}))`,
	}
}

func TestEvaluator_PassFailNoDataError(t *testing.T) {
	baseCfg := &Config{
		Name:          "hc",
		PrometheusURL: "http://prometheus",
		Selector:      labels.SelectorFromSet(labels.Set{"rollout-group": "ingester"}),
		Checks:        []Check{testCheck()},
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
			CandidatePods: TargetPods{Names: []string{"ingester-zone-a-0"}, Zones: []string{"ingester-zone-a"}},
			StablePods:    TargetPods{Names: []string{"ingester-zone-b-0"}, Zones: []string{"ingester-zone-b"}},
			Now:           time.Now(),
			BaselineTime:  time.Now().Add(-time.Hour),
		})
		require.Equal(t, ResultPass, resp.Results["errors"])
		require.Len(t, q.calls, 3)
		require.True(t, strings.Contains(q.calls[0], `pod=~"ingester-zone-a-0"`))
		require.NotContains(t, q.calls[0], `name=~`)
		require.True(t, strings.Contains(q.calls[0], `[1m]`))
		require.True(t, strings.Contains(q.calls[1], `pod=~"ingester-zone-b-0"`))
		require.True(t, strings.Contains(q.calls[1], `[2m]`))
		require.True(t, strings.Contains(q.calls[2], "0.1"))
		require.True(t, strings.Contains(q.calls[2], "0.2"))
	})

	t.Run("fail", func(t *testing.T) {
		q := &fakeQuerier{sequence: []func() (model.Value, error){
			func() (model.Value, error) { return scalar(5), nil },
			func() (model.Value, error) { return scalar(0.1), nil },
			func() (model.Value, error) { return scalar(0), nil },
		}}
		e := NewEvaluator(func(string) (Querier, error) { return q, nil }, nil, log.NewNopLogger())
		resp := e.Evaluate(context.Background(), EvaluateRequest{Config: baseCfg, Namespace: "ns", RolloutGroup: "ingester", Now: time.Now()})
		require.Equal(t, ResultFail, resp.Results["errors"])
	})

	t.Run("no data", func(t *testing.T) {
		q := &fakeQuerier{sequence: []func() (model.Value, error){
			func() (model.Value, error) { return model.Vector{}, nil },
		}}
		e := NewEvaluator(func(string) (Querier, error) { return q, nil }, nil, log.NewNopLogger())
		resp := e.Evaluate(context.Background(), EvaluateRequest{Config: baseCfg, Namespace: "ns", RolloutGroup: "ingester", Now: time.Now()})
		require.Equal(t, ResultNoData, resp.Results["errors"])
	})

	t.Run("error", func(t *testing.T) {
		q := &fakeQuerier{sequence: []func() (model.Value, error){
			func() (model.Value, error) { return nil, errors.New("boom") },
		}}
		e := NewEvaluator(func(string) (Querier, error) { return q, nil }, nil, log.NewNopLogger())
		resp := e.Evaluate(context.Background(), EvaluateRequest{Config: baseCfg, Namespace: "ns", RolloutGroup: "ingester", Now: time.Now()})
		require.Equal(t, ResultError, resp.Results["errors"])
	})

	t.Run("disabled check skipped", func(t *testing.T) {
		cfg := *baseCfg
		cfg.Checks = append([]Check(nil), baseCfg.Checks...)
		cfg.Checks[0].Disabled = true
		q := &fakeQuerier{}
		e := NewEvaluator(func(string) (Querier, error) { return q, nil }, nil, log.NewNopLogger())
		resp := e.Evaluate(context.Background(), EvaluateRequest{Config: &cfg, Namespace: "ns", RolloutGroup: "ingester", Now: time.Now()})
		require.Equal(t, ResultSkipped, resp.Results["errors"])
		require.Empty(t, q.calls)
	})
}

func TestBuildTargetMatchers(t *testing.T) {
	require.Equal(t, `namespace="ns",pod=~"^$"`, buildTargetMatchers("ns", TargetPods{}))
	require.Equal(t, `namespace="ns",pod=~"a-0|b-1"`, buildTargetMatchers("ns", TargetPods{
		Names: []string{"a-0", "b-1"},
		Zones: []string{"a", "b"},
	}))
}
