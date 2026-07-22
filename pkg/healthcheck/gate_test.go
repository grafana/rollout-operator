package healthcheck

import (
	"context"
	"testing"

	"github.com/go-kit/log"
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
