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
	podReadyAnnotationKey = "grafana.com/ready"

	// podReadyAnnotationPatchTimeout bounds the kube API call that maintains podReadyAnnotationKey.
	podReadyAnnotationPatchTimeout = 5 * time.Second
)

// podReadinessTracker maintains the podReadyAnnotationKey annotation on each observed pod
// so that the time since a pod returned to a ready+running state survives rollout-operator
// restarts. The annotation on the pod itself is the source of truth; this struct only
// reflects observed pod state into that annotation.
type podReadinessTracker struct {
	logger     log.Logger
	kubeClient kubernetes.Interface
	namespace  string

	// locks by pod name
	podLocks   map[string]*sync.Mutex
	podLocksMu sync.Mutex
}

func newPodReadinessTracker(kubeClient kubernetes.Interface, namespace string, logger log.Logger) *podReadinessTracker {
	return &podReadinessTracker{
		podLocksMu: sync.Mutex{},
		logger:     logger,
		kubeClient: kubeClient,
		namespace:  namespace,
		podLocks:   map[string]*sync.Mutex{},
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
// live pod. If the pod is ready+running, the live pod is re-fetched and the annotation is set
// to the current time only when it is not already set (so the original timestamp is preserved
// across observer events and rollout-operator restarts). If the pod is not ready+running, the
// annotation is removed. The live re-fetch defends against a stale informer view.
func (c *podReadinessTracker) observed(pod *corev1.Pod) {
	lock := c.podLock(pod.Name)
	lock.Lock()
	defer lock.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), podReadyAnnotationPatchTimeout)
	defer cancel()

	var patch string

	if util.IsPodRunningAndReady(pod) {
		// Get the latest version of this pod - avoid operating on stale observations
		current, err := c.kubeClient.CoreV1().Pods(c.namespace).Get(ctx, pod.Name, metav1.GetOptions{})
		if err != nil {
			level.Warn(c.logger).Log("msg", "failed to get pod for ready annotation check", "pod", pod.Name, "err", err)
			return
		}

		// The pod already has a ready annotation set - do not override that
		if value, ok := current.Annotations[podReadyAnnotationKey]; ok && value != "" {
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

// get returns the time when this pod last transitioned to ready/running.
// This is determined via reading the `podReadyAnnotationKey` annotation value.
// If this annotation has not been set or the pod is not currently ready/running
// then Now() is returned.
//
// Note that the pod is not re-loaded for a staleness check. It is assumed
// that the caller has the latest version of the pod.
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
