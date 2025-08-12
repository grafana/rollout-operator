package zpdb

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/grafana/rollout-operator/pkg/config"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic/fake"
	k8sfake "k8s.io/client-go/kubernetes/fake"
)

// mockLogger implements log.Logger interface for testing
type mockLogger struct{}

func (m mockLogger) Log(keyvals ...interface{}) error {
	return nil
}

type zpdbObserverTestCase struct {
	kubeClient    *k8sfake.Clientset
	dynamicClient *fake.FakeDynamicClient
	logger        mockLogger
	observer      *ZpdbObserver
}

func newZpdbObserverTestCase() *zpdbObserverTestCase {
	test := zpdbObserverTestCase{
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
	test.observer = NewPdbObserver(test.kubeClient, test.dynamicClient, testNamespace, test.logger)
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

// TestZpdbObserver_NewPdbObserver- basic constructor and life cycle test
func TestZpdbObserver_NewPdbObserver(t *testing.T) {
	test := newZpdbObserverTestCase()

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

// TestZpdbObserver_PodEvents validates the pod eviction cache is invalidated on pod changes
func TestZpdbObserver_PodEvents(t *testing.T) {
	test := newZpdbObserverTestCase()
	test.observer.Init()
	defer test.observer.Stop()

	pod := createTestPod("test-pod", testNamespace)

	// Add pod to fake client - this should trigger the informer and invalidate the cache
	test.observer.podEvictCache.RecordEviction(pod)
	require.True(t, test.observer.podEvictCache.HasPendingEviction(pod))
	_, err := test.kubeClient.CoreV1().Pods(testNamespace).Create(context.Background(), pod, metav1.CreateOptions{})
	require.NoError(t, err)
	awaitEviction(t, pod, test)
	require.False(t, test.observer.podEvictCache.HasPendingEviction(pod))

	// Update pod to fake client - this should trigger the informer and invalidate the cache
	test.observer.podEvictCache.RecordEviction(pod)
	require.True(t, test.observer.podEvictCache.HasPendingEviction(pod))
	_, err = test.kubeClient.CoreV1().Pods(testNamespace).Update(context.Background(), pod, metav1.UpdateOptions{})
	require.NoError(t, err)
	awaitEviction(t, pod, test)
	require.False(t, test.observer.podEvictCache.HasPendingEviction(pod))

	// Delete pod to fake client - this should trigger the informer and invalidate the cache
	test.observer.podEvictCache.RecordEviction(pod)
	require.True(t, test.observer.podEvictCache.HasPendingEviction(pod))
	err = test.kubeClient.CoreV1().Pods(testNamespace).Delete(context.Background(), pod.Name, metav1.DeleteOptions{})
	require.NoError(t, err)
	awaitEviction(t, pod, test)
	require.False(t, test.observer.podEvictCache.HasPendingEviction(pod))
}

// TestZpdbObserver_InvalidObject - tests that no panics occur if an invalid object is passed from the informers
func TestZpdbObserver_InvalidObject(t *testing.T) {
	test := newZpdbObserverTestCase()
	test.observer.Init()
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

// TestZpdbObserver_ZPDBEvents_AddValidZPDB - tests the zpdb cache being updated on observed config changes
func TestZpdbObserver_ZPDBEvents_AddValidZPDB(t *testing.T) {
	test := newZpdbObserverTestCase()
	test.observer.Init()
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
	await(t, task, "zpdb config cache has not been initialized")

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
	await(t, task, "zpdb config cache has not been updated")
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
func awaitEviction(t *testing.T, pod *corev1.Pod, test *zpdbObserverTestCase) {
	task := func() bool {
		return !test.observer.podEvictCache.HasPendingEviction(pod)
	}
	await(t, task, "Pod eviction cache has not been invalidated")
}

// await - sleep for a short period and invoke the given task. return if the task returns true. repeat for a fixed number of iterations
func await(t *testing.T, task func() bool, message string) {
	for i := 0; i < 10; i++ {
		time.Sleep(10 * time.Millisecond)
		if task() {
			return
		}
	}
	require.Fail(t, message)
}
