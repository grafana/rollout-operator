package main

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/grafana/rollout-operator/pkg/status"
)

func TestNewDemoController(t *testing.T) {
	c, err := newDemoController()
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
	require.Equal(t, status.PhaseWaiting, byName["ingester"].Members[2].Phase)
	require.Equal(t, "waiting for ingester-zone-b", byName["ingester"].Members[2].Reason)

	require.Equal(t, status.PhaseProgressing, byName["store-gateway"].Phase)
	require.Equal(t, status.PhasePaused, byName["store-gateway"].Members[0].Phase)
	require.True(t, byName["store-gateway"].Members[0].Paused)
	require.Equal(t, status.PhaseProgressing, byName["store-gateway"].Members[1].Phase)

	require.Equal(t, status.PhaseComplete, byName["compactor"].Phase)
}

func TestDemoBlockers(t *testing.T) {
	c, err := newDemoController()
	require.NoError(t, err)
	defer c.Stop()
	go c.Run()
	require.Eventually(t, func() bool {
		snap, err := c.Snapshot(context.Background())
		if err != nil {
			return false
		}
		reasons := map[string]bool{}
		for _, group := range snap.Groups {
			for _, member := range group.Members {
				for _, blocker := range member.Blockers {
					reasons[blocker.Reason] = true
				}
			}
		}
		return reasons["maxUnavailable budget exhausted: 1 unavailable, limit 1"] && reasons["waiting for pod to terminate"] && reasons["ZPDB denied deletion: demo: another zone has a pending eviction"]
	}, time.Second, 10*time.Millisecond)
}
