package zpdb

import (
	"context"
	"testing"
	"time"

	"github.com/go-kit/log"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic/fake"

	"github.com/grafana/rollout-operator/pkg/config"
)

func newConfigObserverTestCase() (*fake.FakeDynamicClient, *ConfigObserver) {
	dynamicClient := fake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{
			{
				Group:    ZoneAwarePodDisruptionBudgetsSpecGroup,
				Version:  ZoneAwarePodDisruptionBudgetsVersion,
				Resource: ZoneAwarePodDisruptionBudgetsNamePlural,
			}: ZoneAwarePodDisruptionBudgetName + "List",
		},
		// No objects passed - will return empty list on query
	)
	observer := NewConfigObserver(dynamicClient, testNamespace, log.NewNopLogger())
	return dynamicClient, observer
}

const (
	testNamespace = "test-namespace"
)

func newPDB(name string) *unstructured.Unstructured {
	ret := unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "rollout-operator.grafana.com/v1",
			"kind":       "ZoneAwarePodDisruptionBudget",
			"metadata": map[string]interface{}{
				"name": name,
			},
			"spec": map[string]interface{}{
				"maxUnavailable": int64(1),
				"selector": map[string]interface{}{
					"matchLabels": map[string]interface{}{
						config.RolloutGroupLabelKey: "test-group",
					},
				},
			},
		},
	}
	ret.SetGeneration(int64(1))
	return &ret
}

// TestObserver_NewPdbObserver- basic constructor and life cycle test
func TestObserver_NewPdbObserver(t *testing.T) {
	_, observer := newConfigObserverTestCase()
	require.NoError(t, observer.Start())

	// Ensure that stopCh has been opened
	select {
	case <-observer.stopCh:
		t.Fatal("stopCh should not be closed initially")
	default:
		// Expected - channel is open
	}

	observer.Stop()

	// Ensure that stopCh has been closed
	select {
	case <-observer.stopCh:
		// Expected - channel is closed
	case <-time.After(100 * time.Millisecond):
		t.Fatal("stopCh should be closed after Stop()")
	}
}

// TestObserver_InvalidObject - tests that no panics occur if an invalid object is passed from the informers
func TestConfigObserver_InvalidObject(t *testing.T) {
	_, observer := newConfigObserverTestCase()
	require.NoError(t, observer.Start())
	defer observer.Stop()

	invalidObj := "not-a-config"

	observer.onPdbAdded(invalidObj)
	observer.onPdbUpdated(invalidObj, invalidObj)
	observer.onPdbDeleted(invalidObj)
}

// TestObserver_ZPDBEvents_AddValidZPDB - tests the zpdb cache being updated on observed config changes
func TestObserver_ZPDBEvents_AddValidZPDB(t *testing.T) {
	dynamicClient, observer := newConfigObserverTestCase()
	require.NoError(t, observer.Start())
	defer observer.Stop()

	require.Equal(t, observer.PdbCache.Size(), 0)

	gvr := schema.GroupVersionResource{
		Group:    ZoneAwarePodDisruptionBudgetsSpecGroup,
		Version:  ZoneAwarePodDisruptionBudgetsVersion,
		Resource: ZoneAwarePodDisruptionBudgetsNamePlural,
	}

	// test triggering a new zpdb being created
	_, err := dynamicClient.Resource(gvr).Namespace(testNamespace).Create(context.Background(), newPDB("test-zpdb"), metav1.CreateOptions{})
	require.NoError(t, err)

	task := func() bool {
		return observer.PdbCache.Size() > 0
	}
	require.Eventually(t, task, time.Second*5, time.Millisecond*10, "zpdb config cache has not been initialized")
	require.Equal(t, 1, observer.PdbCache.Size())
	require.Contains(t, observer.PdbCache.entries, "test-zpdb")
	require.Equal(t, int64(1), observer.PdbCache.entries["test-zpdb"].Generation)

	// test triggering a new zpdb being updated
	updatedPdb := newPDB("test-zpdb")
	updatedPdb.SetGeneration(int64(3))

	_, err = dynamicClient.Resource(gvr).Namespace(testNamespace).Update(context.Background(), updatedPdb, metav1.UpdateOptions{})
	require.NoError(t, err)

	task = func() bool {
		return observer.PdbCache.entries["test-zpdb"].Generation == int64(3)
	}
	require.Eventually(t, task, time.Second*5, time.Millisecond*10, "zpdb config cache has not been updated")
	require.Equal(t, 1, observer.PdbCache.Size())
	require.Equal(t, int64(3), observer.PdbCache.entries["test-zpdb"].Generation)

	// test triggering a stale zpdb update coming in
	stalePdb := newPDB("test-zpdb")
	stalePdb.SetGeneration(int64(2))

	_, err = dynamicClient.Resource(gvr).Namespace(testNamespace).Update(context.Background(), stalePdb, metav1.UpdateOptions{})
	require.NoError(t, err)

	// give long enough that the informer will have fired
	time.Sleep(100 * time.Millisecond)

	require.Equal(t, 1, observer.PdbCache.Size())
	require.Equal(t, int64(3), observer.PdbCache.entries["test-zpdb"].Generation)

	// test stale delete is ignored - calling handler direct as dynamic client delete is limited
	observer.onPdbDeleted(stalePdb)
	require.Equal(t, observer.PdbCache.Size(), 1)

	stalePdb.SetGeneration(int64(5))
	observer.onPdbDeleted(stalePdb)
	require.Equal(t, observer.PdbCache.Size(), 0)
}
