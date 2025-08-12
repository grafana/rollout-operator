package zpdb

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic/fake"
	k8sfake "k8s.io/client-go/kubernetes/fake"

	"github.com/grafana/rollout-operator/pkg/config"
)

// mockLogger implements log.Logger interface for testing
type mockLogger struct{}

func (m mockLogger) Log(keyvals ...interface{}) error {
	return nil
}

type observerTestCase struct {
	kubeClient    *k8sfake.Clientset
	dynamicClient *fake.FakeDynamicClient
	logger        mockLogger
	observer      *Observer
}

func newObserverTestCase() *observerTestCase {
	test := observerTestCase{
		logger:     mockLogger{},
		kubeClient: k8sfake.NewClientset(),
		dynamicClient: fake.NewSimpleDynamicClientWithCustomListKinds(
			runtime.NewScheme(),
			map[schema.GroupVersionResource]string{
				{
					Group:    config.ZoneAwarePodDisruptionBudgetsSpecGroup,
					Version:  config.ZoneAwarePodDisruptionBudgetsVersion,
					Resource: config.ZoneAwarePodDisruptionBudgetsNamePlural,
				}: config.ZoneAwarePodDisruptionBudgetName + "List",
			},
			// No objects passed - will return empty list on query
		),
	}
	test.observer = NewObserver(test.kubeClient, test.dynamicClient, testNamespace, test.logger)
	return &test
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

func createTestPod(name, namespace string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			UID:       types.UID(fmt.Sprintf("uid-%s", name)),
		},
	}
}

// TestObserver_NewPdbObserver- basic constructor and life cycle test
func TestObserver_NewPdbObserver(t *testing.T) {
	test := newObserverTestCase()

	require.NotNil(t, test.observer)
	require.NotNil(t, test.observer.pdbCache)
	require.NotNil(t, test.observer.podEvictCache)
	require.NotNil(t, test.observer.logger)
	require.NotNil(t, test.observer.stopCh)

	require.NoError(t, test.observer.Init())

	// Ensure that stopCh has been opened
	select {
	case <-test.observer.stopCh:
		t.Fatal("stopCh should not be closed initially")
	default:
		// Expected - channel is open
	}

	test.observer.Stop()

	// Ensure that stopCh has been closed
	select {
	case <-test.observer.stopCh:
		// Expected - channel is closed
	case <-time.After(100 * time.Millisecond):
		t.Fatal("stopCh should be closed after Stop()")
	}
}

// TestObserver_PodEvents validates the pod eviction cache is invalidated on pod changes
func TestObserver_PodEvents(t *testing.T) {
	test := newObserverTestCase()
	require.NoError(t, test.observer.Init())
	defer test.observer.Stop()

	pod := createTestPod("test-pod", testNamespace)

	// Add pod to fake client - this should trigger the informer and invalidate the cache
	test.observer.podEvictCache.RecordEviction(pod)
	require.True(t, test.observer.podEvictCache.HasPendingEviction(pod))
	_, err := test.kubeClient.CoreV1().Pods(testNamespace).Create(context.Background(), pod, metav1.CreateOptions{})
	require.NoError(t, err)
	awaitEviction(t, pod, test)

	// Update pod to fake client - this should trigger the informer and invalidate the cache
	test.observer.podEvictCache.RecordEviction(pod)
	require.True(t, test.observer.podEvictCache.HasPendingEviction(pod))
	_, err = test.kubeClient.CoreV1().Pods(testNamespace).Update(context.Background(), pod, metav1.UpdateOptions{})
	require.NoError(t, err)
	awaitEviction(t, pod, test)

	// Delete pod to fake client - this should trigger the informer and invalidate the cache
	test.observer.podEvictCache.RecordEviction(pod)
	require.True(t, test.observer.podEvictCache.HasPendingEviction(pod))
	err = test.kubeClient.CoreV1().Pods(testNamespace).Delete(context.Background(), pod.Name, metav1.DeleteOptions{})
	require.NoError(t, err)
	awaitEviction(t, pod, test)
}

// TestObserver_InvalidObject - tests that no panics occur if an invalid object is passed from the informers
func TestObserver_InvalidObject(t *testing.T) {
	test := newObserverTestCase()
	require.NoError(t, test.observer.Init())
	defer test.observer.Stop()

	invalidObj := "not-a-pod-of-config"

	// These should not panic
	test.observer.onPodAdded(invalidObj)
	test.observer.onPodUpdated(invalidObj, invalidObj)
	test.observer.onPodDeleted(invalidObj)

	test.observer.onPdbAdded(invalidObj)
	test.observer.onPdbUpdated(invalidObj, invalidObj)
	test.observer.onPdbDeleted(invalidObj)
}

// TestObserver_ZPDBEvents_AddValidZPDB - tests the zpdb cache being updated on observed config changes
func TestObserver_ZPDBEvents_AddValidZPDB(t *testing.T) {
	test := newObserverTestCase()
	require.NoError(t, test.observer.Init())
	defer test.observer.Stop()

	require.Equal(t, test.observer.pdbCache.Size(), 0)

	gvr := schema.GroupVersionResource{
		Group:    config.ZoneAwarePodDisruptionBudgetsSpecGroup,
		Version:  config.ZoneAwarePodDisruptionBudgetsVersion,
		Resource: config.ZoneAwarePodDisruptionBudgetsNamePlural,
	}

	// test triggering a new zpdb being created
	_, err := test.dynamicClient.Resource(gvr).Namespace(testNamespace).Create(context.Background(), newPDB("test-zpdb"), metav1.CreateOptions{})
	require.NoError(t, err)

	task := func() bool {
		return test.observer.pdbCache.Size() > 0
	}
	require.Eventually(t, task, time.Second*5, time.Millisecond*10, "zpdb config cache has not been initialized")
	require.Equal(t, 1, test.observer.pdbCache.Size())
	require.Contains(t, test.observer.pdbCache.cache, "test-zpdb")
	require.Equal(t, int64(1), test.observer.pdbCache.cache["test-zpdb"].Generation())

	// test triggering a new zpdb being updated
	updatedPdb := newPDB("test-zpdb")
	updatedPdb.SetGeneration(int64(3))

	_, err = test.dynamicClient.Resource(gvr).Namespace(testNamespace).Update(context.Background(), updatedPdb, metav1.UpdateOptions{})
	require.NoError(t, err)

	task = func() bool {
		return test.observer.pdbCache.cache["test-zpdb"].Generation() == int64(3)
	}
	require.Eventually(t, task, time.Second*5, time.Millisecond*10, "zpdb config cache has not been updated")
	require.Equal(t, 1, test.observer.pdbCache.Size())
	require.Equal(t, int64(3), test.observer.pdbCache.cache["test-zpdb"].Generation())

	// test triggering a stale zpdb update coming in
	stalePdb := newPDB("test-zpdb")
	stalePdb.SetGeneration(int64(2))

	_, err = test.dynamicClient.Resource(gvr).Namespace(testNamespace).Update(context.Background(), stalePdb, metav1.UpdateOptions{})
	require.NoError(t, err)

	// give long enough that the informer will have fired
	time.Sleep(100 * time.Millisecond)

	require.Equal(t, 1, test.observer.pdbCache.Size())
	require.Equal(t, int64(3), test.observer.pdbCache.cache["test-zpdb"].Generation())

	// test stale delete is ignored - calling handler direct as dynamic client delete is limited
	test.observer.onPdbDeleted(stalePdb)
	require.Equal(t, test.observer.pdbCache.Size(), 1)

	stalePdb.SetGeneration(int64(5))
	test.observer.onPdbDeleted(stalePdb)
	require.Equal(t, test.observer.pdbCache.Size(), 0)
}

// awaitEviction awaits a pod to be evicted from the cache - sleeping for a short period and testing the cache a number of times.
func awaitEviction(t *testing.T, pod *corev1.Pod, test *observerTestCase) {
	task := func() bool {
		return !test.observer.podEvictCache.HasPendingEviction(pod)
	}
	require.Eventually(t, task, time.Second*5, time.Millisecond*10, "Awaiting pod eviction")
}
