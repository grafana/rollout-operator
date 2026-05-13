package zpdb

import (
	"context"
	"encoding/json"
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

// podAnnotationsPatch is the strategic merge patch shape for setting or removing a single
// pod annotation. A nil value in the map serializes to JSON null, which strategic merge
// treats as a request to delete the annotation key.
type podAnnotationsPatch struct {
	Metadata podAnnotationsPatchMetadata `json:"metadata"`
}

type podAnnotationsPatchMetadata struct {
	Annotations map[string]*string `json:"annotations"`
}

// podReadinessTracker maintains the podReadyAnnotationKey annotation on each observed pod
// so that the time since a pod returned to a ready+running state survives rollout-operator
// restarts. The annotation on the pod itself is the source of truth; this struct holds no
// in-memory state of its own, just the dependencies needed to read and patch pods.
type podReadinessTracker struct {
	logger       log.Logger
	kubeClient   kubernetes.Interface
	namespace    string
	patchTimeout time.Duration
}

func newPodReadinessTracker(kubeClient kubernetes.Interface, namespace string, patchTimeout time.Duration, logger log.Logger) *podReadinessTracker {
	return &podReadinessTracker{
		logger:       logger,
		kubeClient:   kubeClient,
		namespace:    namespace,
		patchTimeout: patchTimeout,
	}
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
// observed() touches no in-memory tracker state and is safe to call concurrently; the
// SharedInformer's single processor goroutine already serialises informer-driven calls.
func (c *podReadinessTracker) observed(pod *corev1.Pod) {
	ctx, cancel := context.WithTimeout(context.Background(), c.patchTimeout)
	defer cancel()

	// A nil value here means "remove the annotation" (strategic merge interprets null on a map
	// value as a delete). A non-nil value means "set the annotation to *value".
	var value *string

	if util.IsPodRunningAndReady(pod) {
		// Fast path: the informer's in-memory pod copy already shows the annotation, so leave
		// the live pod untouched.
		// If there are further changes to the pod we would expect the informer to fire again
		// and observed() called. Similarly, when the patch is applied this will trigger the
		// informer and observed() to run again ensuring annotation convergence.
		if _, ok := pod.Annotations[podReadyAnnotationKey]; ok {
			return
		}
		value = new(time.Now().UTC().Format(time.RFC3339))
	} else {
		// Fast-path - the pod is not ready and does not have the annotation
		if _, ok := pod.Annotations[podReadyAnnotationKey]; !ok {
			return
		}
	}

	patch, err := json.Marshal(podAnnotationsPatch{
		Metadata: podAnnotationsPatchMetadata{
			Annotations: map[string]*string{podReadyAnnotationKey: value},
		},
	})
	if err != nil {
		// Should not happen for our fixed input shape, but log defensively rather than panic.
		level.Warn(c.logger).Log("msg", "failed to marshal pod ready annotation patch", "pod", pod.Name, "err", err)
		return
	}

	if _, err := c.kubeClient.CoreV1().Pods(c.namespace).Patch(ctx, pod.Name, types.StrategicMergePatchType, patch, metav1.PatchOptions{}); err != nil {
		level.Warn(c.logger).Log("msg", "failed to patch pod ready annotation", "pod", pod.Name, "err", err)
	}
}

// get returns the effective "ready since" time used by the cross-zone eviction delay check.
//
// The return params are as follows;
// * found - we found the `podReadyAnnotationKey` annotation which tracks when this pod last became ready
// * ready+running - the current state of IsPodRunningAndReady() for this pod
// * time - the time the pod last became ready + running - defaults to time.Now() if not found
//
// There are three expected return combinations;
// * true, true, <time> - we found the annotation, the pod is ready+running, and we have the ready since time
// * false, true, time.Now() - we did not find the annotation but the pod is ready+running
// * false, false, time.Now() - the pod is not ready & running
func (c *podReadinessTracker) get(pod *corev1.Pod) (bool, bool, time.Time) {
	if !util.IsPodRunningAndReady(pod) {
		// pod is not ready+running: nothing to report, fall back to now
		return false, false, time.Now()
	}

	readyRunningTime, ok := pod.Annotations[podReadyAnnotationKey]
	if ok && readyRunningTime != "" {
		since, err := time.Parse(time.RFC3339, readyRunningTime)
		if err == nil {
			// annotation found and parsed: pod is ready+running, return the recorded since time
			return true, true, since
		}
	}
	// annotation missing, empty or unparseable: pod is ready+running, fall back to now
	return false, true, time.Now()
}
