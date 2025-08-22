package zpdb

import (
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"

	rolloutconfig "github.com/grafana/rollout-operator/pkg/config"
)

const (
	namespace    = "testnamespace"
	rolloutGroup = "ingester"
	pdbName      = "test-zpdb"
	podName      = "test-1"
)

// TestSelectorMatching confirms that a Config can be found for a given pod
func TestSelectorMatching(t *testing.T) {
	cache := newConfigCache()
	success, _, err := cache.addOrUpdateRaw(rawConfigCacheTest(pdbName, rolloutGroup, 1))
	require.NoError(t, err)
	require.True(t, success)

	success, _, err = cache.addOrUpdateRaw(rawConfigCacheTest("test-zpdb-1", "another-rollout-group", 1))
	require.NoError(t, err)
	require.True(t, success)

	pod := newPodCacheTest(podName, rolloutGroup)
	pdb, err := cache.find(pod)
	require.NoError(t, err)
	require.NotNil(t, pdb)
	require.Equal(t, pdbName, pdb.name)

	pod = newPodCacheTest(podName, "no-match")
	pdb, err = cache.find(pod)
	require.NoError(t, err)
	require.Nil(t, pdb)
}

// TestMultipleSelectorMatches confirms that multiple matching configs for a pod causes an error
func TestMultipleSelectorMatches(t *testing.T) {
	cache := newConfigCache()
	success, _, err := cache.addOrUpdateRaw(rawConfigCacheTest("zpdb-1", rolloutGroup, 1))
	require.NoError(t, err)
	require.True(t, success)
	success, _, err = cache.addOrUpdateRaw(rawConfigCacheTest("zpdb-2", rolloutGroup, 1))
	require.NoError(t, err)
	require.True(t, success)

	pod := newPodCacheTest("test-1", rolloutGroup)
	_, err = cache.find(pod)
	require.ErrorContains(t, err, "multiple zoned pod disruption budgets found for pod")
}

// TestGenerationChecks confirms that stale objects are ignored
func TestGenerationChecks(t *testing.T) {
	cache := newConfigCache()
	raw := rawConfigCacheTest(pdbName, rolloutGroup, 2)
	success, generation, err := cache.addOrUpdateRaw(raw)
	require.NoError(t, err)
	require.Equal(t, int64(2), generation)
	require.True(t, success)

	raw = rawConfigCacheTest(pdbName, rolloutGroup, 1)
	success, generation, err = cache.addOrUpdateRaw(raw)
	require.NoError(t, err)
	require.Equal(t, int64(2), generation)
	require.False(t, success)

	raw = rawConfigCacheTest(pdbName, rolloutGroup, 5)
	success, generation, err = cache.addOrUpdateRaw(raw)
	require.NoError(t, err)
	require.Equal(t, int64(5), generation)
	require.True(t, success)

	success, generation = cache.delete(1, pdbName)
	require.False(t, success)
	require.Equal(t, int64(5), generation)

	success, generation = cache.delete(5, pdbName)
	require.True(t, success)
	require.Equal(t, int64(5), generation)
}

func TestLaterGenerationDelete(t *testing.T) {
	cache := newConfigCache()
	raw := rawConfigCacheTest(pdbName, rolloutGroup, 2)
	success, generation, err := cache.addOrUpdateRaw(raw)
	require.Equal(t, int64(2), generation)
	require.NoError(t, err)
	require.True(t, success)

	success, generation = cache.delete(10, pdbName)
	require.Equal(t, int64(2), generation)
	require.True(t, success)
}

// TestValidationFails confirms that an invalid raw config triggers an error
func TestValidationFails(t *testing.T) {
	cache := newConfigCache()
	raw := rawConfigCacheTest(pdbName, rolloutGroup, 2)
	raw.Object["spec"] = map[string]interface{}{}
	_, _, err := cache.addOrUpdateRaw(raw)
	require.ErrorContains(t, err, "invalid value: selector is not found")
}

func newPodCacheTest(name string, rolloutGroup string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			UID:       types.UID(uuid.New().String()),
			Name:      name,
			Namespace: namespace,
			Labels:    map[string]string{rolloutconfig.RolloutGroupLabelKey: rolloutGroup},
		},
	}
}

func rawConfigCacheTest(name string, rolloutGroup string, generation int64) *unstructured.Unstructured {
	return &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": fmt.Sprintf("%s/%s", ZoneAwarePodDisruptionBudgetsSpecGroup, ZoneAwarePodDisruptionBudgetsVersion),
			"kind":       ZoneAwarePodDisruptionBudgetName,
			"metadata": map[string]interface{}{
				"name":       name,
				"namespace":  namespace,
				"generation": generation,
			},
			"spec": map[string]interface{}{
				"selector": map[string]interface{}{
					"matchLabels": map[string]interface{}{
						rolloutconfig.RolloutGroupLabelKey: rolloutGroup,
					},
				},
			},
		},
	}
}
