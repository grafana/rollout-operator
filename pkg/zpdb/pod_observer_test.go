package zpdb

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/go-kit/log"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	k8sfake "k8s.io/client-go/kubernetes/fake"

	rolloutconfig "github.com/grafana/rollout-operator/pkg/config"
)

func newPodObserverTestCase(t *testing.T) (*k8sfake.Clientset, *podObserver) {
	return newPodObserverTestCaseWithConfig(t, newPDB("test-zpdb"))
}

func newPodObserverTestCaseWithConfig(t *testing.T, rawConfig *unstructured.Unstructured) (*k8sfake.Clientset, *podObserver) {
	client := k8sfake.NewClientset()

	dynamicClient := newFakeDynamicClient()
	cfgObserver := newConfigObserver(dynamicClient, testNamespace, log.NewNopLogger(), NewMetrics(prometheus.NewRegistry()))

	// Seed the config cache with a config that matches the rollout-group used by createTestPod.
	updated, _, err := cfgObserver.pdbCache.addOrUpdateRaw(rawConfig)
	require.NoError(t, err)
	require.True(t, updated)

	observer := newPodObserver(client, testNamespace, newTestPodsFactory(client), 5*time.Second, cfgObserver, log.NewNopLogger())
	return client, observer
}

func TestObserver_TracksReadinessOnlyForCrossZoneEvictionDelay(t *testing.T) {
	testCases := map[string]struct {
		config      *unstructured.Unstructured
		expectPatch bool
	}{
		"delay disabled": {
			config: newPDB("test-zpdb"),
		},
		"delay enabled": {
			config:      rawConfigWithCrossZoneEvictionDelay("test-zpdb", "test-group", 1, 1, `test-(.*)`, "1m"),
			expectPatch: true,
		},
	}

	for name, testCase := range testCases {
		t.Run(name, func(t *testing.T) {
			client, observer := newPodObserverTestCaseWithConfig(t, testCase.config)
			pod := createTestPod("test-pod", testNamespace)
			pod.Status = readyRunningPod(pod.Name).Status
			_, err := client.CoreV1().Pods(testNamespace).Create(context.Background(), pod, metav1.CreateOptions{})
			require.NoError(t, err)
			client.ClearActions()

			observer.onPodAdded(pod)

			patches := 0
			for _, action := range client.Actions() {
				if action.Matches("patch", "pods") {
					patches++
				}
			}
			if testCase.expectPatch {
				require.Equal(t, 1, patches)
			} else {
				require.Zero(t, patches)
			}
		})
	}
}

func TestObserver_ResetsReadinessTimestampBeforeDelayIsReenabled(t *testing.T) {
	client, observer := newPodObserverTestCase(t)
	pod := createTestPod("test-pod", testNamespace)
	pod.Status = readyRunningPod(pod.Name).Status
	pod.Annotations = map[string]string{podReadyAnnotationKey: "2026-01-01T00:00:00Z"}
	_, err := client.CoreV1().Pods(testNamespace).Create(context.Background(), pod, metav1.CreateOptions{})
	require.NoError(t, err)

	observer.onPodAdded(pod)
	pod, err = client.CoreV1().Pods(testNamespace).Get(context.Background(), pod.Name, metav1.GetOptions{})
	require.NoError(t, err)
	require.NotContains(t, pod.Annotations, podReadyAnnotationKey)

	updated, _, err := observer.configObserver.pdbCache.addOrUpdateRaw(
		rawConfigWithCrossZoneEvictionDelay("test-zpdb", "test-group", 2, 1, `test-(.*)`, "1m"),
	)
	require.NoError(t, err)
	require.True(t, updated)

	before := time.Now().UTC()
	observer.onPodUpdated(pod, pod)
	pod, err = client.CoreV1().Pods(testNamespace).Get(context.Background(), pod.Name, metav1.GetOptions{})
	require.NoError(t, err)
	readyTime, err := time.Parse(time.RFC3339, pod.Annotations[podReadyAnnotationKey])
	require.NoError(t, err)
	require.False(t, readyTime.Before(before.Truncate(time.Second)))
}

func TestObserver_RemovesReadinessTimestampOutsideZpdbScope(t *testing.T) {
	client, observer := newPodObserverTestCase(t)
	pod := createTestPod("test-pod", testNamespace)
	pod.Labels[rolloutconfig.RolloutGroupLabelKey] = "other-group"
	pod.Annotations = map[string]string{podReadyAnnotationKey: "2026-01-01T00:00:00Z"}
	_, err := client.CoreV1().Pods(testNamespace).Create(context.Background(), pod, metav1.CreateOptions{})
	require.NoError(t, err)

	observer.onPodAdded(pod)

	pod, err = client.CoreV1().Pods(testNamespace).Get(context.Background(), pod.Name, metav1.GetOptions{})
	require.NoError(t, err)
	require.NotContains(t, pod.Annotations, podReadyAnnotationKey)
}

// TestObserver_SharesThePodInformerFromTheFactory asserts that the observer attaches to the pod informer
// owned by the factory it is given rather than building its own. The rollout controller is handed the same
// factory, so this is what keeps the namespace's pods watched once and cached once.
func TestObserver_SharesThePodInformerFromTheFactory(t *testing.T) {
	client := k8sfake.NewClientset()
	podsFactory := newTestPodsFactory(client)

	cfgObserver := newConfigObserver(newFakeDynamicClient(), testNamespace, log.NewNopLogger(), NewMetrics(prometheus.NewRegistry()))
	observer := newPodObserver(client, testNamespace, podsFactory, 5*time.Second, cfgObserver, log.NewNopLogger())
	require.NoError(t, observer.start())
	defer observer.stop()

	require.Same(t, podsFactory.Core().V1().Pods().Informer(), observer.podsInformer)

	// A second consumer taking pods off the same factory joins the running informer instead of starting a
	// watch of its own.
	watches := 0
	for _, action := range client.Actions() {
		if action.Matches("watch", "pods") {
			watches++
		}
	}
	require.Equal(t, 1, watches)
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
