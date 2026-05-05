package zpdb

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sfake "k8s.io/client-go/kubernetes/fake"
)

// readyRunningPod returns a pod in the testNamespace that passes util.IsPodRunningAndReady.
func readyRunningPod(name string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: testNamespace,
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			ContainerStatuses: []corev1.ContainerStatus{
				{
					Ready: true,
					State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
				},
			},
		},
	}
}

// notReadyPod returns a pod in the testNamespace that fails util.IsPodRunningAndReady.
func notReadyPod(name string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: testNamespace,
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodPending,
		},
	}
}

func TestObserved_SetsAnnotationOnReadyPod(t *testing.T) {
	pod := readyRunningPod("pod-1")
	client := k8sfake.NewClientset(pod)
	tracker := newPodReadinessTracker(client, testNamespace, newDummyLogger())

	before := time.Now().UTC().Truncate(time.Second)
	tracker.observed(pod)
	after := time.Now().UTC().Truncate(time.Second).Add(time.Second)

	got, err := client.CoreV1().Pods(testNamespace).Get(context.Background(), pod.Name, metav1.GetOptions{})
	require.NoError(t, err)

	value := got.Annotations[podReadyAnnotationKey]
	require.NotEmpty(t, value, "ready annotation should be set on a ready pod")

	parsed, err := time.Parse(time.RFC3339, value)
	require.NoError(t, err, "annotation value should parse as RFC3339")
	assert.False(t, parsed.Before(before), "annotation timestamp should be no earlier than the call")
	assert.False(t, parsed.After(after), "annotation timestamp should be no later than the call")
}

func TestObserved_DoesNotOverwriteExistingLiveAnnotation(t *testing.T) {
	// observed() must defer to the live state on the API server when the annotation is already
	// set. This preserves the original ready time across rollout-operator restarts and is also
	// the safety net when the informer view is behind the API server.
	//
	// The in-memory pod has no annotation - the strictest case, since observed() only reads the
	// live pod's annotation. Confirms the live annotation wins regardless of the informer view.
	const existing = "2026-01-01T00:00:00Z"

	live := readyRunningPod("pod-1")
	live.Annotations = map[string]string{podReadyAnnotationKey: existing}
	client := k8sfake.NewClientset(live)
	tracker := newPodReadinessTracker(client, testNamespace, newDummyLogger())

	inMemory := readyRunningPod("pod-1") // no annotation - simulates a stale informer view
	tracker.observed(inMemory)

	got, err := client.CoreV1().Pods(testNamespace).Get(context.Background(), live.Name, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, existing, got.Annotations[podReadyAnnotationKey], "existing live annotation must not be overwritten")
}

func TestObserved_RemovesAnnotationOnNotReadyPod(t *testing.T) {
	pod := notReadyPod("pod-1")
	pod.Annotations = map[string]string{podReadyAnnotationKey: "2026-01-01T00:00:00Z"}
	client := k8sfake.NewClientset(pod)
	tracker := newPodReadinessTracker(client, testNamespace, newDummyLogger())

	tracker.observed(pod)

	got, err := client.CoreV1().Pods(testNamespace).Get(context.Background(), pod.Name, metav1.GetOptions{})
	require.NoError(t, err)
	_, exists := got.Annotations[podReadyAnnotationKey]
	assert.False(t, exists, "ready annotation should be removed when the pod is not ready/running")
}

func TestObserved_PodMissingFromAPIServer_DoesNotPanic(t *testing.T) {
	// observed() must tolerate the GET failing (e.g. NotFound for a pod being deleted).
	client := k8sfake.NewClientset()
	tracker := newPodReadinessTracker(client, testNamespace, newDummyLogger())

	pod := readyRunningPod("pod-missing")
	tracker.observed(pod)
}

func TestObserved_DifferentPodsRunInParallel(t *testing.T) {
	// The per-pod lock must not serialize unrelated pods. Smoke test: many concurrent observed()
	// calls on distinct pods complete without deadlock or interference.
	client := k8sfake.NewClientset()
	tracker := newPodReadinessTracker(client, testNamespace, newDummyLogger())

	const podCount = 16
	pods := make([]*corev1.Pod, podCount)
	for i := range pods {
		pods[i] = readyRunningPod("pod-" + string(rune('a'+i)))
		_, err := client.CoreV1().Pods(testNamespace).Create(context.Background(), pods[i], metav1.CreateOptions{})
		require.NoError(t, err)
	}

	var wg sync.WaitGroup
	wg.Add(len(pods))
	for _, p := range pods {
		go func(p *corev1.Pod) {
			defer wg.Done()
			tracker.observed(p)
		}(p)
	}
	wg.Wait()

	for _, p := range pods {
		got, err := client.CoreV1().Pods(testNamespace).Get(context.Background(), p.Name, metav1.GetOptions{})
		require.NoError(t, err)
		assert.NotEmpty(t, got.Annotations[podReadyAnnotationKey], "every ready pod should have its annotation set")
	}
}

func TestGet_ReturnsAnnotationTimeForReadyPod(t *testing.T) {
	tracker := newPodReadinessTracker(k8sfake.NewClientset(), testNamespace, newDummyLogger())

	want := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	pod := readyRunningPod("pod-1")
	pod.Annotations = map[string]string{podReadyAnnotationKey: want.Format(time.RFC3339)}

	got := tracker.get(pod)
	assert.True(t, want.Equal(got), "get should return the parsed annotation time, want %s got %s", want, got)
}

func TestGet_FallsBackToNow(t *testing.T) {
	// All these inputs route through the "return time.Now()" fallback in get(): either the pod
	// fails the ready/running check or the annotation cannot be used.
	cases := []struct {
		name       string
		pod        *corev1.Pod
		annotation *string // nil means "do not set the annotation key at all"
	}{
		{
			name: "ready pod with no annotation",
			pod:  readyRunningPod("pod-1"),
		},
		{
			name:       "ready pod with empty annotation value",
			pod:        readyRunningPod("pod-1"),
			annotation: ptrString(""),
		},
		{
			name:       "ready pod with malformed annotation value",
			pod:        readyRunningPod("pod-1"),
			annotation: ptrString("not-a-timestamp"),
		},
		{
			name:       "not-ready pod with valid annotation",
			pod:        notReadyPod("pod-1"),
			annotation: ptrString("2026-01-01T00:00:00Z"),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tracker := newPodReadinessTracker(k8sfake.NewClientset(), testNamespace, newDummyLogger())

			if tc.annotation != nil {
				tc.pod.Annotations = map[string]string{podReadyAnnotationKey: *tc.annotation}
			}

			before := time.Now()
			got := tracker.get(tc.pod)
			after := time.Now()

			assert.False(t, got.Before(before), "get should fall back to now (got %s, before %s)", got, before)
			assert.False(t, got.After(after), "get should fall back to now (got %s, after %s)", got, after)
		})
	}
}

func ptrString(s string) *string { return &s }
