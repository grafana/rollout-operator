package healthcheck

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/go-kit/log"
	"github.com/prometheus/common/model"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/tools/record"

	"github.com/grafana/rollout-operator/pkg/config"
)

type staticProvider struct {
	cfg *Config
}

func (s staticProvider) Get(name string) *Config {
	if s.cfg != nil && s.cfg.Name == name {
		return s.cfg
	}
	return nil
}

func TestGate_MisconfiguredProceeds(t *testing.T) {
	recorder := record.NewFakeRecorder(10)
	eval := NewEvaluator(func(string) (Querier, error) {
		t.Fatal("should not query")
		return nil, nil
	}, nil, log.NewNopLogger())
	gate := NewGate(staticProvider{}, eval, nil, recorder, log.NewNopLogger())

	sts := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name: "ingester-zone-b",
			Annotations: map[string]string{
				config.RolloutHealthCheckAnnotationKey: "missing",
			},
			Labels: map[string]string{"rollout-group": "ingester"},
		},
	}
	decision := gate.Evaluate(context.Background(), Request{
		RolloutGroup: "ingester",
		Namespace:    "ns",
		Sets:         []*appsv1.StatefulSet{sts},
		NextSTS:      sts,
	})
	require.False(t, decision.ShouldPause)
	require.Contains(t, decision.Reason, "was not found")
}

func TestGate_SelectorMismatchProceeds(t *testing.T) {
	recorder := record.NewFakeRecorder(10)
	cfg := &Config{
		Name:     "hc",
		Selector: labels.SelectorFromSet(labels.Set{"rollout-group": "other"}),
	}
	gate := NewGate(staticProvider{cfg: cfg}, NewEvaluator(nil, nil, log.NewNopLogger()), nil, recorder, log.NewNopLogger())
	sts := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name: "ingester-zone-b",
			Annotations: map[string]string{
				config.RolloutHealthCheckAnnotationKey: "hc",
			},
			Labels: map[string]string{"rollout-group": "ingester"},
		},
	}
	decision := gate.Evaluate(context.Background(), Request{
		RolloutGroup: "ingester",
		Sets:         []*appsv1.StatefulSet{sts},
		NextSTS:      sts,
	})
	require.False(t, decision.ShouldPause)
	require.Contains(t, decision.Reason, "selector does not match")
}

func TestGate_ConsecutiveFailures(t *testing.T) {
	check := testCheck()
	check.FailurePolicy.ConsecutiveFailures = 3
	check.FailurePolicy.ConsecutiveSuccesses = 1
	check.FailurePolicy.ReevaluationInterval = time.Millisecond
	cfg := &Config{
		Name:          "hc",
		PrometheusURL: "http://prometheus",
		Selector:      labels.SelectorFromSet(labels.Set{"rollout-group": "ingester"}),
		Checks:        []Check{check},
	}

	failSeq := func() []func() (model.Value, error) {
		return []func() (model.Value, error){
			func() (model.Value, error) { return scalar(5), nil },
			func() (model.Value, error) { return scalar(0.1), nil },
			func() (model.Value, error) { return scalar(0), nil },
		}
	}

	q := &fakeQuerier{sequence: failSeq()}
	gate := NewGate(staticProvider{cfg: cfg}, NewEvaluator(func(string) (Querier, error) { return q, nil }, nil, log.NewNopLogger()), nil, record.NewFakeRecorder(10), log.NewNopLogger())
	sts := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "ingester-zone-b",
			Annotations: map[string]string{config.RolloutHealthCheckAnnotationKey: "hc"},
			Labels:      map[string]string{"rollout-group": "ingester"},
		},
	}

	now := time.Now()
	for i := 1; i <= 3; i++ {
		q.sequence = failSeq()
		q.seqIdx = 0
		time.Sleep(2 * time.Millisecond)
		decision := gate.Evaluate(context.Background(), Request{
			RolloutGroup: "ingester",
			Namespace:    "ns",
			NextSTS:      sts,
			Now:          now.Add(time.Duration(i) * time.Second),
		})
		require.True(t, decision.ShouldPause, "iteration %d", i)
		if i < 3 {
			require.Contains(t, decision.Reason, "consecutive failures")
		}
	}
}

func TestGate_PausedDecisionReturnsNextEvaluationDelay(t *testing.T) {
	check := testCheck()
	check.FailurePolicy.ReevaluationInterval = time.Minute
	cfg := &Config{
		Name:          "hc",
		PrometheusURL: "http://prometheus",
		Selector:      labels.SelectorFromSet(labels.Set{"rollout-group": "ingester"}),
		Checks:        []Check{check},
	}
	failSequence := func() []func() (model.Value, error) {
		return []func() (model.Value, error){
			func() (model.Value, error) { return scalar(5), nil },
			func() (model.Value, error) { return scalar(0.1), nil },
			func() (model.Value, error) { return scalar(0), nil },
		}
	}
	q := &fakeQuerier{sequence: failSequence()}
	gate := NewGate(staticProvider{cfg: cfg}, NewEvaluator(func(string) (Querier, error) { return q, nil }, nil, log.NewNopLogger()), nil, nil, log.NewNopLogger())
	sts := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "ingester-zone-b",
			Annotations: map[string]string{config.RolloutHealthCheckAnnotationKey: "hc"},
			Labels:      map[string]string{"rollout-group": "ingester"},
		},
	}
	now := time.Now()
	req := Request{RolloutGroup: "ingester", Namespace: "ns", NextSTS: sts, Now: now}

	decision := gate.Evaluate(context.Background(), req)
	require.True(t, decision.ShouldPause)
	require.Equal(t, time.Minute, decision.RequeueAfter)
	require.Len(t, q.calls, 3)

	req.Now = now.Add(20 * time.Second)
	decision = gate.Evaluate(context.Background(), req)
	require.True(t, decision.ShouldPause)
	require.Equal(t, 40*time.Second, decision.RequeueAfter)
	require.Len(t, q.calls, 3, "cached decision must not query before the deadline")

	q.sequence = failSequence()
	q.seqIdx = 0
	req.Now = now.Add(time.Minute)
	decision = gate.Evaluate(context.Background(), req)
	require.True(t, decision.ShouldPause)
	require.Equal(t, time.Minute, decision.RequeueAfter)
	require.Len(t, q.calls, 6)
}

func TestGate_LogsPolicyOutcomeWithoutQueries(t *testing.T) {
	check := testCheck()
	cfg := &Config{
		Name:          "hc",
		PrometheusURL: "http://prometheus",
		Selector:      labels.SelectorFromSet(labels.Set{"rollout-group": "ingester"}),
		Checks:        []Check{check},
	}
	q := &fakeQuerier{sequence: []func() (model.Value, error){
		func() (model.Value, error) { return scalar(5), nil },
		func() (model.Value, error) { return scalar(0.1), nil },
		func() (model.Value, error) { return scalar(0), nil },
	}}
	var logs bytes.Buffer
	logger := log.NewLogfmtLogger(&logs)
	gate := NewGate(staticProvider{cfg: cfg}, NewEvaluator(func(string) (Querier, error) { return q, nil }, nil, logger), nil, nil, logger)
	sts := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "ingester-zone-b",
			Annotations: map[string]string{config.RolloutHealthCheckAnnotationKey: "hc"},
			Labels:      map[string]string{"rollout-group": "ingester"},
		},
	}

	decision := gate.Evaluate(context.Background(), Request{
		RolloutGroup: "ingester",
		Namespace:    "ns",
		NextSTS:      sts,
		Now:          time.Now(),
	})

	require.True(t, decision.ShouldPause)
	output := logs.String()
	require.Contains(t, output, "msg=\"health check policy applied\"")
	require.Contains(t, output, "policy=hc")
	require.Contains(t, output, "check=errors")
	require.Contains(t, output, "action=pause")
	require.Contains(t, output, "current_value=5")
	require.Contains(t, output, "baseline_value=0.1")
	require.Contains(t, output, "consecutive_failures=1")
	require.False(t, strings.Contains(output, check.Query))
	require.False(t, strings.Contains(output, check.SuccessQuery))
}

func TestGate_WarnOnFailureExceeded(t *testing.T) {
	check := testCheck()
	check.FailurePolicy.ConsecutiveFailures = 1
	check.FailurePolicy.ExceededAction = ActionWarn
	check.FailurePolicy.ReevaluationInterval = time.Millisecond
	cfg := &Config{
		Name:          "hc",
		PrometheusURL: "http://prometheus",
		Selector:      labels.SelectorFromSet(labels.Set{"rollout-group": "ingester"}),
		Checks:        []Check{check},
	}
	q := &fakeQuerier{sequence: []func() (model.Value, error){
		func() (model.Value, error) { return scalar(5), nil },
		func() (model.Value, error) { return scalar(0.1), nil },
		func() (model.Value, error) { return scalar(0), nil },
	}}
	gate := NewGate(staticProvider{cfg: cfg}, NewEvaluator(func(string) (Querier, error) { return q, nil }, nil, log.NewNopLogger()), nil, record.NewFakeRecorder(10), log.NewNopLogger())
	sts := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "ingester-zone-b",
			Annotations: map[string]string{config.RolloutHealthCheckAnnotationKey: "hc"},
			Labels:      map[string]string{"rollout-group": "ingester"},
		},
	}
	decision := gate.Evaluate(context.Background(), Request{RolloutGroup: "ingester", Namespace: "ns", NextSTS: sts, Now: time.Now()})
	require.False(t, decision.ShouldPause)
	require.NotEmpty(t, decision.Reason)
}

func TestGate_ErrorRetriesAcrossReconciles(t *testing.T) {
	check := testCheck()
	check.ErrorPolicy.MaxAttempts = 3
	check.ErrorPolicy.RetryInterval = time.Millisecond
	check.ErrorPolicy.ExhaustedAction = ActionPause
	cfg := &Config{
		Name:          "hc",
		PrometheusURL: "http://prometheus",
		Selector:      labels.SelectorFromSet(labels.Set{"rollout-group": "ingester"}),
		Checks:        []Check{check},
	}
	q := &fakeQuerier{sequence: []func() (model.Value, error){
		func() (model.Value, error) { return nil, errors.New("boom") },
	}}
	gate := NewGate(staticProvider{cfg: cfg}, NewEvaluator(func(string) (Querier, error) { return q, nil }, nil, log.NewNopLogger()), nil, record.NewFakeRecorder(10), log.NewNopLogger())
	sts := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "ingester-zone-b",
			Annotations: map[string]string{config.RolloutHealthCheckAnnotationKey: "hc"},
			Labels:      map[string]string{"rollout-group": "ingester"},
		},
	}
	now := time.Now()
	for i := 1; i <= 3; i++ {
		q.seqIdx = 0
		q.sequence = []func() (model.Value, error){
			func() (model.Value, error) { return nil, errors.New("boom") },
		}
		decision := gate.Evaluate(context.Background(), Request{
			RolloutGroup: "ingester",
			Namespace:    "ns",
			NextSTS:      sts,
			Now:          now.Add(time.Duration(i) * time.Second),
		})
		require.True(t, decision.ShouldPause, "attempt %d", i)
		if i < 3 {
			require.Contains(t, decision.Reason, "attempt")
		} else {
			require.Contains(t, decision.Reason, "after 3 attempts")
		}
	}
}

func TestGate_ConsecutiveSuccessesRequired(t *testing.T) {
	check := testCheck()
	check.FailurePolicy.ConsecutiveSuccesses = 2
	check.FailurePolicy.ConsecutiveFailures = 3
	check.FailurePolicy.ReevaluationInterval = time.Millisecond
	cfg := &Config{
		Name:          "hc",
		PrometheusURL: "http://prometheus",
		Selector:      labels.SelectorFromSet(labels.Set{"rollout-group": "ingester"}),
		Checks:        []Check{check},
	}
	passSeq := func() []func() (model.Value, error) {
		return []func() (model.Value, error){
			func() (model.Value, error) { return scalar(0.1), nil },
			func() (model.Value, error) { return scalar(0.2), nil },
			func() (model.Value, error) { return scalar(1), nil },
		}
	}
	q := &fakeQuerier{sequence: passSeq()}
	gate := NewGate(staticProvider{cfg: cfg}, NewEvaluator(func(string) (Querier, error) { return q, nil }, nil, log.NewNopLogger()), nil, record.NewFakeRecorder(10), log.NewNopLogger())
	sts := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "ingester-zone-b",
			Annotations: map[string]string{config.RolloutHealthCheckAnnotationKey: "hc"},
			Labels:      map[string]string{"rollout-group": "ingester"},
		},
	}
	now := time.Now()
	q.sequence = passSeq()
	decision := gate.Evaluate(context.Background(), Request{RolloutGroup: "ingester", Namespace: "ns", NextSTS: sts, Now: now})
	require.True(t, decision.ShouldPause)
	require.Contains(t, decision.Reason, "consecutive successes")

	q.seqIdx = 0
	q.sequence = passSeq()
	decision = gate.Evaluate(context.Background(), Request{RolloutGroup: "ingester", Namespace: "ns", NextSTS: sts, Now: now.Add(time.Second)})
	require.False(t, decision.ShouldPause)
	require.Empty(t, decision.Reason)
}
