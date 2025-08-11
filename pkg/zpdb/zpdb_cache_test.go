package zpdb

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"

	"github.com/grafana/rollout-operator/pkg/config"
)

const (
	namespace    = "testnamespace"
	rolloutGroup = "ingester"
	pdbName      = "test-zpdb"
	podName      = "test-1"
)

// TestSelectorMatching confirms that a ZpdbConfig can be found for a given pod
func TestSelectorMatching(t *testing.T) {
	cache := NewZpdbCache()
	success, err := cache.AddOrUpdateRaw(rawConfigCacheTest(pdbName, rolloutGroup, 1))
	require.NoError(t, err)
	require.True(t, success)

	success, err = cache.AddOrUpdateRaw(rawConfigCacheTest("test-zpdb-1", "another-rollout-group", 1))
	require.NoError(t, err)
	require.True(t, success)

	pod := newPodCacheTest(podName, rolloutGroup)
	pdb, err := cache.Find(pod)
	require.NoError(t, err)
	require.NotNil(t, pdb)
	require.Equal(t, pdbName, pdb.Name())

	pod = newPodCacheTest(podName, "no-match")
	pdb, err = cache.Find(pod)
	require.NoError(t, err)
	require.Nil(t, pdb)
}

// TestMultipleSelectorMatches confirms that multiple matching ZpdbConfigs for a pod causes an error
func TestMultipleSelectorMatches(t *testing.T) {
	cache := NewZpdbCache()
	success, err := cache.AddOrUpdateRaw(rawConfigCacheTest("zpdb-1", rolloutGroup, 1))
	require.NoError(t, err)
	require.True(t, success)
	success, err = cache.AddOrUpdateRaw(rawConfigCacheTest("zpdb-2", rolloutGroup, 1))
	require.NoError(t, err)
	require.True(t, success)

	pod := newPodCacheTest("test-1", rolloutGroup)
	_, err = cache.Find(pod)
	require.ErrorContains(t, err, "multiple zoned pod disruption budgets found for pod")
}

// TestGenerationChecks confirms that stale objects are ignored
func TestGenerationChecks(t *testing.T) {
	cache := NewZpdbCache()
	raw := rawConfigCacheTest(pdbName, rolloutGroup, 2)
	success, err := cache.AddOrUpdateRaw(raw)
	require.NoError(t, err)
	require.True(t, success)

	raw = rawConfigCacheTest(pdbName, rolloutGroup, 1)
	success, err = cache.AddOrUpdateRaw(raw)
	require.NoError(t, err)
	require.False(t, success)

	raw = rawConfigCacheTest(pdbName, rolloutGroup, 5)
	success, err = cache.AddOrUpdateRaw(raw)
	require.NoError(t, err)
	require.True(t, success)

	require.False(t, cache.Delete(1, pdbName))
	require.True(t, cache.Delete(5, pdbName))
}

// TestValidationFails confirms that an invalid raw config triggers an error
func TestValidationFails(t *testing.T) {
	cache := NewZpdbCache()
	raw := rawConfigCacheTest(pdbName, rolloutGroup, 2)
	raw.Object["spec"] = map[string]interface{}{}
	_, err := cache.AddOrUpdateRaw(raw)
	require.ErrorContains(t, err, "invalid value - selector is not found")
}

func newPodCacheTest(name string, rolloutGroup string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			UID:       types.UID(uuid.New().String()),
			Name:      name,
			Namespace: namespace,
			Labels:    map[string]string{config.RolloutGroupLabelKey: rolloutGroup},
		},
	}
}

func rawConfigCacheTest(name string, rolloutGroup string, generation int64) *unstructured.Unstructured {
	return &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "rollout-operator.grafana.com/v1",
			"kind":       "ZoneAwarePodDisruptionBudget",
			"metadata": map[string]interface{}{
				"name":       name,
				"namespace":  namespace,
				"generation": generation,
			},
			"spec": map[string]interface{}{
				"selector": map[string]interface{}{
					"matchLabels": map[string]interface{}{
						config.RolloutGroupLabelKey: rolloutGroup,
					},
				},
			},
		},
	}
}
