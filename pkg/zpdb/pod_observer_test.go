package zpdb

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/go-kit/log"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	k8sfake "k8s.io/client-go/kubernetes/fake"

	rolloutconfig "github.com/grafana/rollout-operator/pkg/config"
)

func newPodObserverTestCase(t *testing.T) (*k8sfake.Clientset, *podObserver) {
	client := k8sfake.NewClientset()

	metrics := NewMetrics(prometheus.NewRegistry())
	dynamicClient := newFakeDynamicClient()
	cfgObserver := newConfigObserver(dynamicClient, testNamespace, log.NewNopLogger(), metrics)

	// Seed the config cache with a config that matches the rollout-group used by createTestPod.
	updated, _, err := cfgObserver.pdbCache.addOrUpdateRaw(newPDB("test-zpdb"))
	require.NoError(t, err)
	require.True(t, updated)

	observer := newPodObserver(client, testNamespace, 5*time.Second, cfgObserver, metrics, log.NewNopLogger())
	return client, observer
}

func createTestPod(name, namespace string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			UID:       types.UID(fmt.Sprintf("uid-%s", name)),
			Labels:    map[string]string{rolloutconfig.RolloutGroupLabelKey: "test-group"},
		},
	}
}

// TestObserver_NewPdbObserver- basic constructor and life cycle test
func TestObserver_NewPodObserver(t *testing.T) {
	_, observer := newPodObserverTestCase(t)

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

// TestObserver_PodEvents validates the pod eviction cache is invalidated on pod changes
func TestObserver_PodEvents(t *testing.T) {
	client, observer := newPodObserverTestCase(t)
	require.NoError(t, observer.start())
	defer observer.stop()

	// This pod will not pass a ready & running test.
	pod := createTestPod("test-pod", testNamespace)

	// Add pod to fake client - this should trigger the informer and invalidate the cache
	observer.podEvictCache.recordEviction(pod)
	require.True(t, observer.podEvictCache.hasPendingEviction(pod))
	_, err := client.CoreV1().Pods(testNamespace).Create(context.Background(), pod, metav1.CreateOptions{})
	require.NoError(t, err)
	awaitEviction(t, pod, observer)

	// Update pod to fake client - this should trigger the informer and invalidate the cache
	observer.podEvictCache.recordEviction(pod)
	require.True(t, observer.podEvictCache.hasPendingEviction(pod))
	_, err = client.CoreV1().Pods(testNamespace).Update(context.Background(), pod, metav1.UpdateOptions{})
	require.NoError(t, err)
	awaitEviction(t, pod, observer)

	// Delete pod to fake client - this should trigger the informer and invalidate the cache
	observer.podEvictCache.recordEviction(pod)
	require.True(t, observer.podEvictCache.hasPendingEviction(pod))
	err = client.CoreV1().Pods(testNamespace).Delete(context.Background(), pod.Name, metav1.DeleteOptions{})
	require.NoError(t, err)
	awaitEviction(t, pod, observer)
}

// TestObserver_InvalidObject - tests that no panics occur if an invalid object is passed from the informers
func TestPodObserver_InvalidObject(t *testing.T) {
	_, observer := newPodObserverTestCase(t)
	require.NoError(t, observer.start())
	defer observer.stop()

	invalidObj := "not-a-pod"

	// These should not panic
	observer.onPodAdded(invalidObj)
	observer.onPodUpdated(invalidObj, invalidObj)
	observer.onPodDeleted(invalidObj)
}

// TestObserver_IgnorePodEvents validates the pod eviction cache is not invalidated until the pod phase changes
func TestObserver_IgnorePodEvents(t *testing.T) {
	_, observer := newPodObserverTestCase(t)
	require.NoError(t, observer.start())
	defer observer.stop()

	pod := createTestPod("test-pod", testNamespace)
	pod.Status.Phase = corev1.PodRunning

	observer.podEvictCache.recordEviction(pod)
	require.True(t, observer.podEvictCache.hasPendingEviction(pod))
	observer.onPodAdded(pod)
	require.True(t, observer.podEvictCache.hasPendingEviction(pod))
	observer.onPodUpdated(pod, pod)
	require.True(t, observer.podEvictCache.hasPendingEviction(pod))

	pod.Status.Phase = corev1.PodFailed
	observer.onPodUpdated(pod, pod)
	require.False(t, observer.podEvictCache.hasPendingEviction(pod))
}

// awaitEviction awaits a pod to be evicted from the cache
func awaitEviction(t *testing.T, pod *corev1.Pod, observer *podObserver) {
	task := func() bool {
		return !observer.podEvictCache.hasPendingEviction(pod)
	}
	require.Eventually(t, task, time.Second*5, time.Millisecond*10, "Awaiting pod eviction")
}

// TestObserver_InformerEventTimestamp covers the signal that reveals a pod watch which has silently stopped
// delivering updates. Because the eviction webhook reads pod state from the informer cache, that is the only
// thing standing between a dead watch and eviction decisions made on indefinitely stale data.
func TestObserver_InformerEventTimestamp(t *testing.T) {
	lastEvent := func(o *podObserver) float64 {
		return testutil.ToFloat64(o.metrics.PodInformerLastEventTime)
	}

	t.Run("published once the cache has synced", func(t *testing.T) {
		_, observer := newPodObserverTestCase(t)
		require.Zero(t, lastEvent(observer))

		require.NoError(t, observer.start())
		defer observer.stop()

		// Set even in a namespace with no pods, so the series is never simply absent.
		require.NotZero(t, lastEvent(observer))
	})

	for name, event := range map[string]func(o *podObserver, pod *corev1.Pod){
		"add":    func(o *podObserver, pod *corev1.Pod) { o.onPodAdded(pod) },
		"delete": func(o *podObserver, pod *corev1.Pod) { o.onPodDeleted(pod) },
		"update": func(o *podObserver, pod *corev1.Pod) {
			updated := pod.DeepCopy()
			updated.ResourceVersion = "2"
			o.onPodUpdated(pod, updated)
		},
	} {
		t.Run("recorded on "+name, func(t *testing.T) {
			_, observer := newPodObserverTestCase(t)
			pod := createTestPod("test-pod", testNamespace)
			pod.ResourceVersion = "1"

			event(observer, pod)
			require.NotZero(t, lastEvent(observer))
		})
	}

	t.Run("advances as the informer delivers changes", func(t *testing.T) {
		client, observer := newPodObserverTestCase(t)
		require.NoError(t, observer.start())
		defer observer.stop()

		// Drive the informer for real rather than calling the handlers directly, so this covers the whole
		// path the metric is meant to attest to: a change reaches the watch, the cache is updated, and the
		// timestamp moves.
		pods := client.CoreV1().Pods(testNamespace)
		pod := createTestPod("test-pod", testNamespace)

		// The resource versions are set by hand because the fake clientset's tracker does not maintain them,
		// and an update whose resource version has not moved is indistinguishable from a resync. A real
		// apiserver bumps it on every mutation.
		pod.ResourceVersion = "1"

		for _, change := range []struct {
			name  string
			apply func() error
		}{
			{"create", func() error {
				_, err := pods.Create(context.Background(), pod, metav1.CreateOptions{})
				return err
			}},
			{"update", func() error {
				pod.Status.Phase = corev1.PodRunning
				pod.ResourceVersion = "2"
				_, err := pods.Update(context.Background(), pod, metav1.UpdateOptions{})
				return err
			}},
			{"delete", func() error {
				return pods.Delete(context.Background(), pod.Name, metav1.DeleteOptions{})
			}},
		} {
			before := lastEvent(observer)
			require.NoError(t, change.apply(), change.name)
			require.Eventually(t, func() bool { return lastEvent(observer) > before }, time.Second*5, time.Millisecond*10,
				"informer event timestamp should advance on pod "+change.name)
		}
	})

	t.Run("not recorded on a resync", func(t *testing.T) {
		_, observer := newPodObserverTestCase(t)
		pod := createTestPod("test-pod", testNamespace)
		pod.ResourceVersion = "1"

		// A resync replays the cached pod with an unchanged resource version. It is served from the cache
		// and keeps arriving on the factory's resync interval even when the watch is dead, so counting it
		// would make a broken watch look healthy.
		observer.onPodUpdated(pod, pod.DeepCopy())
		require.Zero(t, lastEvent(observer))
	})
}
