package controller

import (
	"testing"
	"time"

	"github.com/go-kit/log"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/apps/v1"

	"github.com/grafana/rollout-operator/pkg/config"
)

func TestGetMostRecentDownscale(t *testing.T) {
	t.Run("no last downscale", func(t *testing.T) {
		sts1 := mockStatefulSet("test-zone-a")
		sts2 := mockStatefulSet("test-zone-b")
		sts3 := mockStatefulSet("test-zone-c")

		lastDownscale, err := getMostRecentDownscale(sts1, []*v1.StatefulSet{sts1, sts2, sts3})
		require.NoError(t, err)
		require.Zero(t, lastDownscale)
	})

	t.Run("malformed downscale time", func(t *testing.T) {
		sts1 := mockStatefulSet("test-zone-a")
		sts2 := mockStatefulSet("test-zone-b")
		sts3 := mockStatefulSet("test-zone-c", withAnnotations(map[string]string{
			config.LastDownscaleAnnotationKey: "invalid",
		}))

		_, err := getMostRecentDownscale(sts1, []*v1.StatefulSet{sts1, sts2, sts3})
		require.Error(t, err)
	})

	t.Run("downscale set on one sts", func(t *testing.T) {
		downscale := time.Now().UTC().Round(time.Second).Add(-24 * time.Hour)

		sts1 := mockStatefulSet("test-zone-a")
		sts2 := mockStatefulSet("test-zone-b")
		sts3 := mockStatefulSet("test-zone-c", withAnnotations(map[string]string{
			config.LastDownscaleAnnotationKey: downscale.Format(time.RFC3339),
		}))

		lastDownscale, err := getMostRecentDownscale(sts1, []*v1.StatefulSet{sts1, sts2, sts3})
		require.NoError(t, err)
		require.Equal(t, downscale, lastDownscale)
	})

	t.Run("compare multiple downscales", func(t *testing.T) {
		downscale1 := time.Now().UTC().Round(time.Second).Add(-12 * time.Hour)
		downscale2 := time.Now().UTC().Round(time.Second).Add(-24 * time.Hour)

		sts1 := mockStatefulSet("test-zone-a")
		sts2 := mockStatefulSet("test-zone-b", withAnnotations(map[string]string{
			config.LastDownscaleAnnotationKey: downscale1.Format(time.RFC3339),
		}))
		sts3 := mockStatefulSet("test-zone-c", withAnnotations(map[string]string{
			config.LastDownscaleAnnotationKey: downscale2.Format(time.RFC3339),
		}))

		lastDownscale, err := getMostRecentDownscale(sts1, []*v1.StatefulSet{sts1, sts2, sts3})
		require.NoError(t, err)
		require.Equal(t, downscale1, lastDownscale)
	})
}

func TestMinimumTimeHasElapsed(t *testing.T) {
	t.Run("no last downscale", func(t *testing.T) {
		sts1 := mockStatefulSet("test-zone-a")
		sts2 := mockStatefulSet("test-zone-b")
		sts3 := mockStatefulSet("test-zone-c")

		elapsed, err := minimumTimeHasElapsed(sts1, []*v1.StatefulSet{sts1, sts2, sts3}, log.NewNopLogger())
		require.NoError(t, err)
		require.True(t, elapsed)
	})

	t.Run("missing min time config", func(t *testing.T) {
		downscale1 := time.Now().UTC().Round(time.Second).Add(-12 * time.Hour)
		downscale2 := time.Now().UTC().Round(time.Second).Add(-24 * time.Hour)

		sts1 := mockStatefulSet("test-zone-a")
		sts2 := mockStatefulSet("test-zone-b", withAnnotations(map[string]string{
			config.LastDownscaleAnnotationKey: downscale1.Format(time.RFC3339),
		}))
		sts3 := mockStatefulSet("test-zone-c", withAnnotations(map[string]string{
			config.LastDownscaleAnnotationKey: downscale2.Format(time.RFC3339),
		}))

		_, err := minimumTimeHasElapsed(sts1, []*v1.StatefulSet{sts1, sts2, sts3}, log.NewNopLogger())
		require.Error(t, err)
	})

	t.Run("invalid min time config", func(t *testing.T) {
		downscale1 := time.Now().UTC().Round(time.Second).Add(-12 * time.Hour)
		downscale2 := time.Now().UTC().Round(time.Second).Add(-24 * time.Hour)

		sts1 := mockStatefulSet("test-zone-a", withLabels(map[string]string{
			config.MinTimeBetweenZonesDownscaleLabelKey: "invalid",
		}))
		sts2 := mockStatefulSet("test-zone-b", withAnnotations(map[string]string{
			config.LastDownscaleAnnotationKey: downscale1.Format(time.RFC3339),
		}))
		sts3 := mockStatefulSet("test-zone-c", withAnnotations(map[string]string{
			config.LastDownscaleAnnotationKey: downscale2.Format(time.RFC3339),
		}))

		_, err := minimumTimeHasElapsed(sts1, []*v1.StatefulSet{sts1, sts2, sts3}, log.NewNopLogger())
		require.Error(t, err)
	})

	t.Run("not enough time", func(t *testing.T) {
		downscale1 := time.Now().UTC().Round(time.Second).Add(-10 * time.Hour)
		downscale2 := time.Now().UTC().Round(time.Second).Add(-24 * time.Hour)

		sts1 := mockStatefulSet("test-zone-a", withLabels(map[string]string{
			config.MinTimeBetweenZonesDownscaleLabelKey: "12h",
		}))
		sts2 := mockStatefulSet("test-zone-b", withAnnotations(map[string]string{
			config.LastDownscaleAnnotationKey: downscale1.Format(time.RFC3339),
		}))
		sts3 := mockStatefulSet("test-zone-c", withAnnotations(map[string]string{
			config.LastDownscaleAnnotationKey: downscale2.Format(time.RFC3339),
		}))

		elapsed, err := minimumTimeHasElapsed(sts1, []*v1.StatefulSet{sts1, sts2, sts3}, log.NewNopLogger())
		require.NoError(t, err)
		require.False(t, elapsed)
	})

	t.Run("enough time", func(t *testing.T) {
		downscale1 := time.Now().UTC().Round(time.Second).Add(-14 * time.Hour)
		downscale2 := time.Now().UTC().Round(time.Second).Add(-26 * time.Hour)

		sts1 := mockStatefulSet("test-zone-a", withLabels(map[string]string{
			config.MinTimeBetweenZonesDownscaleLabelKey: "12h",
		}))
		sts2 := mockStatefulSet("test-zone-b", withAnnotations(map[string]string{
			config.LastDownscaleAnnotationKey: downscale1.Format(time.RFC3339),
		}))
		sts3 := mockStatefulSet("test-zone-c", withAnnotations(map[string]string{
			config.LastDownscaleAnnotationKey: downscale2.Format(time.RFC3339),
		}))

		elapsed, err := minimumTimeHasElapsed(sts1, []*v1.StatefulSet{sts1, sts2, sts3}, log.NewNopLogger())
		require.NoError(t, err)
		require.True(t, elapsed)
	})
}

func TestGetLeaderForStatefulSet(t *testing.T) {
	t.Run("no leader config", func(t *testing.T) {
		sts1 := mockStatefulSet("test-zone-a")
		sts2 := mockStatefulSet("test-zone-b")
		sts3 := mockStatefulSet("test-zone-c")

		leader, err := getLeaderForStatefulSet(sts1, []*v1.StatefulSet{sts1, sts2, sts3})
		require.Nil(t, leader)
		require.NoError(t, err)
	})

	t.Run("no leader matches", func(t *testing.T) {
		sts1 := mockStatefulSet("test-zone-a")
		sts2 := mockStatefulSet("test-zone-b", withAnnotations(map[string]string{
			config.RolloutDownscaleLeaderAnnotationKey: "test-zone-z",
		}))
		sts3 := mockStatefulSet("test-zone-c")

		leader, err := getLeaderForStatefulSet(sts2, []*v1.StatefulSet{sts1, sts2, sts3})
		require.Nil(t, leader)
		require.Error(t, err)
	})

	t.Run("leader matches", func(t *testing.T) {
		sts1 := mockStatefulSet("test-zone-a")
		sts2 := mockStatefulSet("test-zone-b", withAnnotations(map[string]string{
			config.RolloutDownscaleLeaderAnnotationKey: "test-zone-a",
		}))
		sts3 := mockStatefulSet("test-zone-c")

		leader, err := getLeaderForStatefulSet(sts2, []*v1.StatefulSet{sts1, sts2, sts3})
		require.NotNil(t, leader)
		require.Equal(t, sts1.GetName(), leader.GetName())
		require.NoError(t, err)
	})
}

func TestReconcileStsReplicas(t *testing.T) {
	t.Run("no leader", func(t *testing.T) {
		sts1 := mockStatefulSet("test-zone-a")
		sts2 := mockStatefulSet("test-zone-b")
		sts3 := mockStatefulSet("test-zone-c")

		replicas, err := desiredStsReplicas("test", sts2, []*v1.StatefulSet{sts1, sts2, sts3}, log.NewNopLogger())
		require.NoError(t, err)
		require.Equal(t, int32(3), replicas)
	})

	t.Run("scale up", func(t *testing.T) {
		sts1 := mockStatefulSet("test-zone-a", withReplicas(4, 4))
		sts2 := mockStatefulSet("test-zone-b", withReplicas(3, 3), withAnnotations(map[string]string{
			config.RolloutDownscaleLeaderAnnotationKey: "test-zone-a",
		}))
		sts3 := mockStatefulSet("test-zone-c", withReplicas(3, 3), withAnnotations(map[string]string{
			config.RolloutDownscaleLeaderAnnotationKey: "test-zone-b",
		}))

		replicas, err := desiredStsReplicas("test", sts2, []*v1.StatefulSet{sts1, sts2, sts3}, log.NewNopLogger())
		require.NoError(t, err)
		require.Equal(t, int32(4), replicas)
	})

	t.Run("scale up only when all leader replicas are ready", func(t *testing.T) {
		sts1 := mockStatefulSet("test-zone-a", withReplicas(4, 3))
		sts2 := mockStatefulSet("test-zone-b", withReplicas(2, 2), withAnnotations(map[string]string{
			config.RolloutDownscaleLeaderAnnotationKey: "test-zone-a",
			config.RolloutLeaderReadyAnnotationKey:     config.RolloutLeaderReadyAnnotationValue,
		}))
		sts3 := mockStatefulSet("test-zone-c", withReplicas(1, 1), withAnnotations(map[string]string{
			config.RolloutDownscaleLeaderAnnotationKey: "test-zone-b",
			config.RolloutLeaderReadyAnnotationKey:     config.RolloutLeaderReadyAnnotationValue,
		}))

		replicas, err := desiredStsReplicas("test", sts2, []*v1.StatefulSet{sts1, sts2, sts3}, log.NewNopLogger())
		require.NoError(t, err)
		require.Equal(t, int32(2), replicas, "no change in replicas because leader isn't ready yet")

		sts1 = mockStatefulSet("test-zone-a", withReplicas(4, 4))
		replicasB, err := desiredStsReplicas("test", sts2, []*v1.StatefulSet{sts1, sts2, sts3}, log.NewNopLogger())
		require.NoError(t, err)
		require.Equal(t, int32(4), replicasB, "ready to scale zone-b")

		sts2 = mockStatefulSet("test-zone-b", withReplicas(replicasB, 2))
		replicasC, err := desiredStsReplicas("test", sts3, []*v1.StatefulSet{sts1, sts2, sts3}, log.NewNopLogger())
		require.NoError(t, err)
		require.Equal(t, int32(1), replicasC, "no change in replicas because zone-b isn't ready yet")

		sts2 = mockStatefulSet("test-zone-b", withReplicas(replicasB, replicasB))
		replicasC, err = desiredStsReplicas("test", sts3, []*v1.StatefulSet{sts1, sts2, sts3}, log.NewNopLogger())
		require.NoError(t, err)
		require.Equal(t, int32(4), replicasC, "ready to scale zone-c")
	})

	t.Run("scale up ignoring ready replicas", func(t *testing.T) {
		sts1 := mockStatefulSet("test-zone-a", withReplicas(4, 2))
		sts2 := mockStatefulSet("test-zone-b", withReplicas(3, 3), withAnnotations(map[string]string{
			config.RolloutDownscaleLeaderAnnotationKey: "test-zone-a",
		}))

		replicas, err := desiredStsReplicas("test", sts2, []*v1.StatefulSet{sts1, sts2}, log.NewNopLogger())
		require.NoError(t, err)
		require.Equal(t, int32(4), replicas)
	})

	t.Run("scaling down always occurs to desired replicas", func(t *testing.T) {
		sts1 := mockStatefulSet("test-zone-a", withReplicas(2, 1))
		sts2 := mockStatefulSet("test-zone-b", withReplicas(3, 3), withAnnotations(map[string]string{
			config.RolloutDownscaleLeaderAnnotationKey: "test-zone-a",
			config.RolloutLeaderReadyAnnotationKey:     config.RolloutLeaderReadyAnnotationValue,
		}))

		replicas, err := desiredStsReplicas("test", sts2, []*v1.StatefulSet{sts1, sts2}, log.NewNopLogger())
		require.NoError(t, err)
		require.Equal(t, int32(2), replicas)
	})

	t.Run("scale down min time error", func(t *testing.T) {
		downscale1 := time.Now().UTC().Round(time.Second).Add(-72 * time.Hour)
		downscale2 := time.Now().UTC().Round(time.Second).Add(-60 * time.Hour)
		downscale3 := time.Now().UTC().Round(time.Second).Add(-48 * time.Hour)

		sts1 := mockStatefulSet("test-zone-a", withReplicas(2, 2), withAnnotations(map[string]string{
			config.LastDownscaleAnnotationKey: downscale1.Format(time.RFC3339),
		}), withLabels(map[string]string{
			config.MinTimeBetweenZonesDownscaleLabelKey: "12h",
		}))
		sts2 := mockStatefulSet("test-zone-b", withReplicas(3, 3), withAnnotations(map[string]string{
			config.RolloutDownscaleLeaderAnnotationKey: "test-zone-a",
			config.LastDownscaleAnnotationKey:          downscale2.Format(time.RFC3339),
		}), withLabels(map[string]string{
			config.MinTimeBetweenZonesDownscaleLabelKey: "invalid",
		}))
		sts3 := mockStatefulSet("test-zone-c", withReplicas(3, 3), withAnnotations(map[string]string{
			config.RolloutDownscaleLeaderAnnotationKey: "test-zone-b",
			config.LastDownscaleAnnotationKey:          downscale3.Format(time.RFC3339),
		}), withLabels(map[string]string{
			config.MinTimeBetweenZonesDownscaleLabelKey: "12h",
		}))

		replicas, err := desiredStsReplicas("test", sts2, []*v1.StatefulSet{sts1, sts2, sts3}, log.NewNopLogger())
		require.Error(t, err)
		require.Equal(t, int32(3), replicas)
	})

	t.Run("scale down min time not passed", func(t *testing.T) {
		downscale1 := time.Now().UTC().Round(time.Second).Add(-34 * time.Hour)
		downscale2 := time.Now().UTC().Round(time.Second).Add(-22 * time.Hour)
		downscale3 := time.Now().UTC().Round(time.Second).Add(-10 * time.Hour)

		sts1 := mockStatefulSet("test-zone-a", withReplicas(2, 2), withAnnotations(map[string]string{
			config.LastDownscaleAnnotationKey: downscale1.Format(time.RFC3339),
		}), withLabels(map[string]string{
			config.MinTimeBetweenZonesDownscaleLabelKey: "12h",
		}))
		sts2 := mockStatefulSet("test-zone-b", withReplicas(3, 3), withAnnotations(map[string]string{
			config.RolloutDownscaleLeaderAnnotationKey: "test-zone-a",
			config.LastDownscaleAnnotationKey:          downscale2.Format(time.RFC3339),
		}), withLabels(map[string]string{
			config.MinTimeBetweenZonesDownscaleLabelKey: "12h",
		}))
		sts3 := mockStatefulSet("test-zone-c", withReplicas(3, 3), withAnnotations(map[string]string{
			config.RolloutDownscaleLeaderAnnotationKey: "test-zone-b",
			config.LastDownscaleAnnotationKey:          downscale3.Format(time.RFC3339),
		}), withLabels(map[string]string{
			config.MinTimeBetweenZonesDownscaleLabelKey: "12h",
		}))

		replicas, err := desiredStsReplicas("test", sts2, []*v1.StatefulSet{sts1, sts2, sts3}, log.NewNopLogger())
		require.NoError(t, err)
		require.Equal(t, int32(3), replicas)
	})

	t.Run("scale down min time passed", func(t *testing.T) {
		downscale1 := time.Now().UTC().Round(time.Second).Add(-72 * time.Hour)
		downscale2 := time.Now().UTC().Round(time.Second).Add(-60 * time.Hour)
		downscale3 := time.Now().UTC().Round(time.Second).Add(-48 * time.Hour)

		sts1 := mockStatefulSet("test-zone-a", withReplicas(2, 2), withAnnotations(map[string]string{
			config.LastDownscaleAnnotationKey: downscale1.Format(time.RFC3339),
		}), withLabels(map[string]string{
			config.MinTimeBetweenZonesDownscaleLabelKey: "12h",
		}))
		sts2 := mockStatefulSet("test-zone-b", withReplicas(3, 3), withAnnotations(map[string]string{
			config.RolloutDownscaleLeaderAnnotationKey: "test-zone-a",
			config.LastDownscaleAnnotationKey:          downscale2.Format(time.RFC3339),
		}), withLabels(map[string]string{
			config.MinTimeBetweenZonesDownscaleLabelKey: "12h",
		}))
		sts3 := mockStatefulSet("test-zone-c", withReplicas(3, 3), withAnnotations(map[string]string{
			config.RolloutDownscaleLeaderAnnotationKey: "test-zone-b",
			config.LastDownscaleAnnotationKey:          downscale3.Format(time.RFC3339),
		}), withLabels(map[string]string{
			config.MinTimeBetweenZonesDownscaleLabelKey: "12h",
		}))

		replicas, err := desiredStsReplicas("test", sts2, []*v1.StatefulSet{sts1, sts2, sts3}, log.NewNopLogger())
		require.NoError(t, err)
		require.Equal(t, int32(2), replicas)
	})
}
