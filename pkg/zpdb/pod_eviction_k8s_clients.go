package zpdb

import (
	"context"
	"errors"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/client-go/kubernetes"

	"github.com/grafana/rollout-operator/pkg/util"
)

// A k8sClient holds the Kubernetes API client used to query Kubernetes
type k8sClient struct {
	ctx context.Context
	// The client we use to query for StatefulSets and Pods
	kubeClient kubernetes.Interface
}

// podByName searches and returns a Pod for the given namespace and name
func (a *k8sClient) podByName(namespace string, name string) (*corev1.Pod, error) {
	if pod, err := a.kubeClient.CoreV1().Pods(namespace).Get(a.ctx, name, metav1.GetOptions{}); err != nil {
		return nil, err
	} else if pod == nil {
		return nil, errors.New("pod not found")
	} else {
		return pod, nil
	}
}

// owner returns the StatefulSet which manages a pod or an error if the owner can not be found or is not a StatefulSet
func (a *k8sClient) owner(pod *corev1.Pod) (*appsv1.StatefulSet, error) {
	owner := metav1.GetControllerOf(pod)
	if owner == nil {
		return nil, errors.New("unable to find a StatefulSet pod owner")
	} else if owner.Kind != "StatefulSet" {
		return nil, fmt.Errorf("pod owner is not a StatefulSet - %s has owner %s (%s)", pod.Name, owner.Name, owner.Kind)
	}

	if sts, err := a.kubeClient.AppsV1().StatefulSets(pod.Namespace).Get(a.ctx, owner.Name, metav1.GetOptions{}); err != nil {
		return nil, fmt.Errorf("unable to find StatefulSet %s by name: %w", owner.Name, err)
	} else if sts == nil {
		return nil, fmt.Errorf("unable to find StatefulSet %s by name", owner.Name)
	} else {
		return sts, nil
	}
}

// findRelatedStatefulSets returns all StatefulSets which match the given Selector.
func (a *k8sClient) findRelatedStatefulSets(namespace string, selector *labels.Selector) (*appsv1.StatefulSetList, error) {
	return a.kubeClient.AppsV1().StatefulSets(namespace).List(a.ctx, metav1.ListOptions{LabelSelector: (*selector).String()})
}

// podsNotRunningAndReady finds the pods managed by a given StatefulSet. Each pod is inspected to see if it is ready and running. A tally of the total number of pods and the number not ready/running is returned.
// It is possible for pods to be in a state where they are not yet returned by the pod listing. These pods should be considered and are reported as unknown.
// The number of unknown pods is determined as the difference between the StatefulSets Replica count minus the number of pods listed.
// The given Pod is excluded from testing, but is included in the total of tested pods
func (a *k8sClient) podsNotRunningAndReady(sts *appsv1.StatefulSet, pod *corev1.Pod, matcher partitionMatcher, evictionCache *podEvictionCache) (*zoneStatusResult, error) {
	podsSelector := labels.NewSelector().Add(
		util.MustNewLabelsRequirement("name", selection.Equals, []string{sts.Spec.Template.Labels["name"]}),
	)

	list, err := a.kubeClient.CoreV1().Pods(sts.Namespace).List(a.ctx, metav1.ListOptions{
		LabelSelector: podsSelector.String(),
	})

	if err != nil {
		return nil, err
	}

	result := &zoneStatusResult{tested: 0, notReady: 0, unknown: 0}

	// replicas is the number of Pods created by the StatefulSet controller.
	// Spec.Replicas - the desired number of pods as set from config or by a controller
	// Status.Replicas - the number of pods created by the Stateful set
	// In a normal running state these values will be equal.
	// If the system is up-scaled or down-scaled the Spec.Replicas will initially increase or decrease and eventually the Status.Replicas will converge
	// Taking the max value errs on the side of caution
	var replicas int
	if sts.Spec.Replicas == nil {
		replicas = int(sts.Status.Replicas)
	} else {
		replicas = max(int(sts.Status.Replicas), int(*sts.Spec.Replicas))
	}

	for _, pd := range list.Items {

		// we do not consider pods which are in a different partition
		if !matcher(&pd) {
			continue
		}

		if pod.UID != pd.UID && (evictionCache.hasPendingEviction(&pd) || !util.IsPodRunningAndReady(&pd)) {
			// if a pod has recently been evicted then we assume it is not ready
			// this is avoiding a possible race condition of concurrent eviction requests are occurring and an eviction has not yet caused a pod state change
			result.notReady++
		}

		result.tested++
	}

	// we consider the pod as not ready if there should be a given replica count but it is not yet being found in the pods query
	// note that the effect here is that we do not know which partition these other pods will be in, so we have to attribute them to this partition to be safe
	if len(list.Items) < replicas {
		result.unknown = replicas - len(list.Items)
	}

	return result, nil
}
