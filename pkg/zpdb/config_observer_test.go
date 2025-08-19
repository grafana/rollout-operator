package zpdb

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/go-kit/log"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic/fake"

	rolloutconfig "github.com/grafana/rollout-operator/pkg/config"
)

const (
	testNamespace = "test-namespace"
)

func newFakeDynamicClient() *fake.FakeDynamicClient {
	return fake.NewSimpleDynamicClientWithCustomListKinds(
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
}

func newConfigObserverTestCase() (*fake.FakeDynamicClient, *configObserver) {
	dynamicClient := newFakeDynamicClient()
	observer := newConfigObserver(dynamicClient, testNamespace, log.NewNopLogger())
	return dynamicClient, observer
}

func newPDB(name string) *unstructured.Unstructured {
	ret := unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": fmt.Sprintf("%s/%s", ZoneAwarePodDisruptionBudgetsSpecGroup, ZoneAwarePodDisruptionBudgetsVersion),
			"kind":       ZoneAwarePodDisruptionBudgetName,
			"metadata": map[string]interface{}{
				"name": name,
			},
			"spec": map[string]interface{}{
				FieldMaxUnavailable: int64(1),
				FieldSelector: map[string]interface{}{
					FieldMatchLabels: map[string]interface{}{
						rolloutconfig.RolloutGroupLabelKey: "test-group",
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
	require.NoError(t, observer.start())

	select {
	case <-observer.stopCh:
		t.Fatal("stopCh should not be closed initially")
	default:

	}

	observer.stop()

	select {
	case <-observer.stopCh:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("stopCh should be closed after Stop()")
	}
}

// TestObserver_InvalidObject - tests that no panics occur if an invalid object is passed from the informers
func TestConfigObserver_InvalidObject(t *testing.T) {
	_, observer := newConfigObserverTestCase()
	require.NoError(t, observer.start())
	defer observer.stop()

	invalidObj := "not-a-config"

	observer.onPdbAdded(invalidObj)
	observer.onPdbUpdated(invalidObj, invalidObj)
	observer.onPdbDeleted(invalidObj)
}

// TestObserver_ZPDBEvents_AddValidZPDB - tests the zpdb configCache being updated on observed config changes
func TestObserver_ZPDBEvents_AddValidZPDB(t *testing.T) {
	dynamicClient, observer := newConfigObserverTestCase()
	require.NoError(t, observer.start())
	defer observer.stop()

	require.Equal(t, observer.pdbCache.size(), 0)

	gvr := schema.GroupVersionResource{
		Group:    ZoneAwarePodDisruptionBudgetsSpecGroup,
		Version:  ZoneAwarePodDisruptionBudgetsVersion,
		Resource: ZoneAwarePodDisruptionBudgetsNamePlural,
	}

	// test triggering a new zpdb being created
	_, err := dynamicClient.Resource(gvr).Namespace(testNamespace).Create(context.Background(), newPDB("test-zpdb"), metav1.CreateOptions{})
	require.NoError(t, err)

	task := func() bool {
		return observer.pdbCache.size() > 0
	}
	require.Eventually(t, task, time.Second*5, time.Millisecond*10, "zpdb config configCache has not been initialized")
	require.Equal(t, 1, observer.pdbCache.size())
	require.Contains(t, observer.pdbCache.entries, "test-zpdb")
	require.Equal(t, int64(1), observer.pdbCache.entries["test-zpdb"].generation)

	// test triggering a new zpdb being updated
	updatedPdb := newPDB("test-zpdb")
	updatedPdb.SetGeneration(int64(3))

	_, err = dynamicClient.Resource(gvr).Namespace(testNamespace).Update(context.Background(), updatedPdb, metav1.UpdateOptions{})
	require.NoError(t, err)

	task = func() bool {
		return observer.pdbCache.entries["test-zpdb"].generation == int64(3)
	}
	require.Eventually(t, task, time.Second*5, time.Millisecond*10, "zpdb config configCache has not been updated")
	require.Equal(t, 1, observer.pdbCache.size())
	require.Equal(t, int64(3), observer.pdbCache.entries["test-zpdb"].generation)

	// test triggering a stale zpdb update coming in
	stalePdb := newPDB("test-zpdb")
	stalePdb.SetGeneration(int64(2))

	_, err = dynamicClient.Resource(gvr).Namespace(testNamespace).Update(context.Background(), stalePdb, metav1.UpdateOptions{})
	require.NoError(t, err)

	require.Eventually(t, func() bool { return observer.pdbCache.size() == 1 }, time.Second*1, time.Millisecond*10)
	require.Equal(t, int64(3), observer.pdbCache.entries["test-zpdb"].generation)

	// test stale delete is ignored - calling handler direct as dynamic client delete is limited
	observer.onPdbDeleted(stalePdb)
	require.Equal(t, observer.pdbCache.size(), 1)

	stalePdb.SetGeneration(int64(5))
	observer.onPdbDeleted(stalePdb)
	require.Equal(t, observer.pdbCache.size(), 0)
}
