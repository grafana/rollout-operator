package controller

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/grafana/rollout-operator/pkg/status"
)

func TestNewDemoController(t *testing.T) {
	c, err := NewDemoController()
	require.NoError(t, err)
	defer c.Stop()

	snap, err := c.Snapshot(context.Background())
	require.NoError(t, err)
	require.Equal(t, demoNamespace, snap.Namespace)
	require.Len(t, snap.Groups, 3)

	byName := map[string]status.Group{}
	for _, g := range snap.Groups {
		byName[g.Name] = g
	}

	require.Equal(t, status.PhaseProgressing, byName["ingester"].Phase)
	require.Len(t, byName["ingester"].Members, 3)
	require.Equal(t, status.PhaseComplete, byName["ingester"].Members[0].Phase)
	require.Equal(t, status.PhaseProgressing, byName["ingester"].Members[1].Phase)
	require.Equal(t, status.PhaseProgressing, byName["ingester"].Members[2].Phase)

	require.Equal(t, status.PhaseProgressing, byName["store-gateway"].Phase)
	require.Equal(t, status.PhasePaused, byName["store-gateway"].Members[0].Phase)
	require.True(t, byName["store-gateway"].Members[0].Paused)
	require.Equal(t, status.PhaseProgressing, byName["store-gateway"].Members[1].Phase)

	require.Equal(t, status.PhaseComplete, byName["compactor"].Phase)
}
