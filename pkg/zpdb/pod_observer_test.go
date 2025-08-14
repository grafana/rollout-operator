package zpdb

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/go-kit/log"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	k8sfake "k8s.io/client-go/kubernetes/fake"
)

func newPodObserverTestCase() (*k8sfake.Clientset, *PodObserver) {
	client := k8sfake.NewClientset()
	observer := NewPodObserver(client, testNamespace, log.NewNopLogger())
	return client, observer
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
	_, observer := newPodObserverTestCase()

	require.NotNil(t, observer)
	require.NotNil(t, observer.PodEvictCache)
	require.NotNil(t, observer.podsFactory)
	require.NotNil(t, observer.podsInformer)
	require.NotNil(t, observer.logger)
	require.NotNil(t, observer.stopCh)
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

// TestObserver_PodEvents validates the pod eviction cache is invalidated on pod changes
func TestObserver_PodEvents(t *testing.T) {
	client, observer := newPodObserverTestCase()
	require.NoError(t, observer.Start())
	defer observer.Stop()

	pod := createTestPod("test-pod", testNamespace)

	// Add pod to fake client - this should trigger the informer and invalidate the cache
	observer.PodEvictCache.RecordEviction(pod)
	require.True(t, observer.PodEvictCache.HasPendingEviction(pod))
	_, err := client.CoreV1().Pods(testNamespace).Create(context.Background(), pod, metav1.CreateOptions{})
	require.NoError(t, err)
	awaitEviction(t, pod, observer)

	// Update pod to fake client - this should trigger the informer and invalidate the cache
	observer.PodEvictCache.RecordEviction(pod)
	require.True(t, observer.PodEvictCache.HasPendingEviction(pod))
	_, err = client.CoreV1().Pods(testNamespace).Update(context.Background(), pod, metav1.UpdateOptions{})
	require.NoError(t, err)
	awaitEviction(t, pod, observer)

	// Delete pod to fake client - this should trigger the informer and invalidate the cache
	observer.PodEvictCache.RecordEviction(pod)
	require.True(t, observer.PodEvictCache.HasPendingEviction(pod))
	err = client.CoreV1().Pods(testNamespace).Delete(context.Background(), pod.Name, metav1.DeleteOptions{})
	require.NoError(t, err)
	awaitEviction(t, pod, observer)
}

// TestObserver_InvalidObject - tests that no panics occur if an invalid object is passed from the informers
func TestPodObserver_InvalidObject(t *testing.T) {
	_, observer := newPodObserverTestCase()
	require.NoError(t, observer.Start())
	defer observer.Stop()

	invalidObj := "not-a-pod"

	// These should not panic
	observer.onPodAdded(invalidObj)
	observer.onPodUpdated(invalidObj, invalidObj)
	observer.onPodDeleted(invalidObj)
}

// awaitEviction awaits a pod to be evicted from the cache - sleeping for a short period and testing the cache a number of times.
func awaitEviction(t *testing.T, pod *corev1.Pod, observer *PodObserver) {
	task := func() bool {
		return !observer.PodEvictCache.HasPendingEviction(pod)
	}
	require.Eventually(t, task, time.Second*5, time.Millisecond*10, "Awaiting pod eviction")
}
