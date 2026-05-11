package zpdb

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/go-kit/log"
	"github.com/go-kit/log/level"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"

	"github.com/grafana/rollout-operator/pkg/util"
)

const (
	// podReadyAnnotationKey is set on a pod to record when it was first observed as ready and running.
	// The value is the observation time formatted as RFC3339 (UTC).
	// Once set, the annotation is preserved across subsequent ready observations so the original
	// timestamp is retained. The annotation is removed when the pod transitions out of a ready+running state.
	podReadyAnnotationKey = "grafana.com/ready-time"
)

// podReadinessTracker maintains the podReadyAnnotationKey annotation on each observed pod
// so that the time since a pod returned to a ready+running state survives rollout-operator
// restarts. The annotation on the pod itself is the source of truth; this struct only
// reflects observed pod state into that annotation.
type podReadinessTracker struct {
	logger       log.Logger
	kubeClient   kubernetes.Interface
	namespace    string
	patchTimeout time.Duration

	// locks by pod name
	podLocks   map[string]*sync.Mutex
	podLocksMu sync.Mutex
}

func newPodReadinessTracker(kubeClient kubernetes.Interface, namespace string, patchTimeout time.Duration, logger log.Logger) *podReadinessTracker {
	return &podReadinessTracker{
		podLocksMu:   sync.Mutex{},
		logger:       logger,
		kubeClient:   kubeClient,
		namespace:    namespace,
		patchTimeout: patchTimeout,
		podLocks:     map[string]*sync.Mutex{},
	}
}

// podLock returns the per-pod mutex.
// Mutexes are created on demand and retained for the lifetime of the tracker, since pod
// names are bounded by deployment cardinality and each mutex is small.
func (c *podReadinessTracker) podLock(podName string) *sync.Mutex {
	c.podLocksMu.Lock()
	defer c.podLocksMu.Unlock()

	if m, ok := c.podLocks[podName]; ok {
		return m
	}
	m := &sync.Mutex{}
	c.podLocks[podName] = m
	return m
}

// observed reflects the pod's ready state into the `podReadyAnnotationKey` annotation on the
// live pod.
//
// When the pod is ready+running:
//   - If the in-memory pod already carries the annotation, return (fast path). This preserves
//     the original timestamp across observer events and rollout-operator restarts, and avoids
//     unnecessary patches for unrelated status updates and the watch event triggered by our
//     own prior patch.
//   - Otherwise, patch the annotation to the current time.
//
// When the pod is not ready+running, the annotation is removed.
//
// observed() is dispatched serially by the SharedInformer's single processor goroutine; the
// per-pod lock guards against any future callers outside the informer path.
func (c *podReadinessTracker) observed(pod *corev1.Pod) {
	lock := c.podLock(pod.Name)
	lock.Lock()
	defer lock.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), c.patchTimeout)
	defer cancel()

	var patch string

	if util.IsPodRunningAndReady(pod) {
		// Fast path: the informer's in-memory pod copy already shows the annotation, so leave
		// the live pod untouched.
		// If there are further changes to the pod we would expect the informer to fire again
		// and observed() called. Similarly, when the patch is applied this will trigger the
		// informer and observed() to run again ensuring annotation convergence.
		if value, ok := pod.Annotations[podReadyAnnotationKey]; ok && value != "" {
			return
		}

		// set the ready annotation value to Now()
		patch = fmt.Sprintf(`{"metadata":{"annotations":{%q:%q}}}`, podReadyAnnotationKey, time.Now().UTC().Format(time.RFC3339))

	} else {
		// null out the ready annotation
		patch = fmt.Sprintf(`{"metadata":{"annotations":{%q:null}}}`, podReadyAnnotationKey)
	}

	if _, err := c.kubeClient.CoreV1().Pods(c.namespace).Patch(ctx, pod.Name, types.StrategicMergePatchType, []byte(patch), metav1.PatchOptions{}); err != nil {
		level.Warn(c.logger).Log("msg", "failed to patch pod ready annotation", "pod", pod.Name, "err", err)
	}
}

// get returns the effective "ready since" time used by the cross-zone eviction delay check.
//
// For a ready+running pod, the value is parsed from the `podReadyAnnotationKey` annotation
// (i.e. when we first observed the pod as ready). If the annotation is absent, empty, or
// unparseable, or if the pod is not currently ready+running, get returns time.Now() - this is
// the safe default that keeps the delay window open until a fresh observation lands.
//
// The pod is not re-loaded; the caller is expected to pass the latest known version (typically
// the one supplied by the informer event that prompted the eviction admission check).
func (c *podReadinessTracker) get(pod *corev1.Pod) time.Time {
	lock := c.podLock(pod.Name)
	lock.Lock()
	defer lock.Unlock()

	if util.IsPodRunningAndReady(pod) {
		readyRunningTime, ok := pod.Annotations[podReadyAnnotationKey]
		if ok && readyRunningTime != "" {
			since, err := time.Parse(time.RFC3339, readyRunningTime)
			if err == nil {
				return since
			}
		}
	}

	return time.Now()
}
