package zpdb

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	k8sfake "k8s.io/client-go/kubernetes/fake"
)

type podObserverTestCase struct {
	kubeClient *k8sfake.Clientset
	logger     mockLogger
	observer   *PodObserver
}

func newPodObserverTestCase() *podObserverTestCase {
	test := podObserverTestCase{
		logger:     mockLogger{},
		kubeClient: k8sfake.NewClientset(),
	}
	test.observer = NewPodObserver(test.kubeClient, testNamespace, test.logger)
	return &test
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
func TestObserver_NewPodObserver(t *testing.T) {
	test := newPodObserverTestCase()

	require.NotNil(t, test.observer)
	require.NotNil(t, test.observer.PodEvictCache)
	require.NotNil(t, test.observer.podsFactory)
	require.NotNil(t, test.observer.podsInformer)
	require.NotNil(t, test.observer.logger)
	require.NotNil(t, test.observer.stopCh)
	require.NoError(t, test.observer.Start())

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
	test := newPodObserverTestCase()
	require.NoError(t, test.observer.Start())
	defer test.observer.Stop()

	pod := createTestPod("test-pod", testNamespace)

	// Add pod to fake client - this should trigger the informer and invalidate the cache
	test.observer.PodEvictCache.RecordEviction(pod)
	require.True(t, test.observer.PodEvictCache.HasPendingEviction(pod))
	_, err := test.kubeClient.CoreV1().Pods(testNamespace).Create(context.Background(), pod, metav1.CreateOptions{})
	require.NoError(t, err)
	awaitEviction(t, pod, test)

	// Update pod to fake client - this should trigger the informer and invalidate the cache
	test.observer.PodEvictCache.RecordEviction(pod)
	require.True(t, test.observer.PodEvictCache.HasPendingEviction(pod))
	_, err = test.kubeClient.CoreV1().Pods(testNamespace).Update(context.Background(), pod, metav1.UpdateOptions{})
	require.NoError(t, err)
	awaitEviction(t, pod, test)

	// Delete pod to fake client - this should trigger the informer and invalidate the cache
	test.observer.PodEvictCache.RecordEviction(pod)
	require.True(t, test.observer.PodEvictCache.HasPendingEviction(pod))
	err = test.kubeClient.CoreV1().Pods(testNamespace).Delete(context.Background(), pod.Name, metav1.DeleteOptions{})
	require.NoError(t, err)
	awaitEviction(t, pod, test)
}

// TestObserver_InvalidObject - tests that no panics occur if an invalid object is passed from the informers
func TestPodObserver_InvalidObject(t *testing.T) {
	test := newPodObserverTestCase()
	require.NoError(t, test.observer.Start())
	defer test.observer.Stop()

	invalidObj := "not-a-pod"

	// These should not panic
	test.observer.onPodAdded(invalidObj)
	test.observer.onPodUpdated(invalidObj, invalidObj)
	test.observer.onPodDeleted(invalidObj)
}

// awaitEviction awaits a pod to be evicted from the cache - sleeping for a short period and testing the cache a number of times.
func awaitEviction(t *testing.T, pod *corev1.Pod, test *podObserverTestCase) {
	task := func() bool {
		return !test.observer.PodEvictCache.HasPendingEviction(pod)
	}
	require.Eventually(t, task, time.Second*5, time.Millisecond*10, "Awaiting pod eviction")
}
