package admission

import (
	"context"
	"errors"
	"fmt"
	"github.com/grafana/rollout-operator/pkg/config"
	"github.com/grafana/rollout-operator/pkg/util"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
)

// A podStatusResult holds the status of a pod availability within a zone / StatefulSet
type podStatusResult struct {
	// the number of pods tested for their status
	tested int
	// the number of pods who are not ready/running
	notReady int
	// the number of pods we do not know their status
	unknown int
}

// A k8sClients holds the Kubernetes API clients used to query Kubernetes
type k8sClients struct {
	ctx context.Context
	// The client we use to query for StatefulSets and Pods
	kubeClient kubernetes.Interface
	// The client we use to query for the custom resource used for PDB configuration
	dynamicClient dynamic.Interface
}

// podByName searches and returns a Pod for the given namespace and name
func (a *k8sClients) podByName(namespace string, name string) (*corev1.Pod, error) {
	return a.kubeClient.CoreV1().Pods(namespace).Get(a.ctx, name, metav1.GetOptions{})
}

// owner returns the StatefulSet which manages a pod or an error if the owner can not be found or is not a StatefulSet
func (a *k8sClients) owner(pod *corev1.Pod) (*appsv1.StatefulSet, error) {
	if owner := metav1.GetControllerOf(pod); owner != nil && owner.Kind == "StatefulSet" {
		var sts *appsv1.StatefulSet
		var err error
		if sts, err = a.kubeClient.AppsV1().StatefulSets(pod.Namespace).Get(a.ctx, owner.Name, metav1.GetOptions{}); err != nil {
			return nil, err
		}
		return sts, nil
	}
	return nil, errors.New("unable to find a StatefulSet pod owner")
}

// findRelatedStatefulSets returns all StatefulSets which match the given Selector.
// If the selector is not set, a fallback selector is created which matches StatefulSets with the same rollout-group label as the given StatefulSet
func (a *k8sClients) findRelatedStatefulSets(sts *appsv1.StatefulSet, selector *labels.Selector) (*appsv1.StatefulSetList, error) {

	if selector == nil {
		// fall back to finding stateful sets in the same rollout_group
		rolloutGroup, exists := sts.Labels[config.RolloutGroupLabelKey]
		if !exists || len(rolloutGroup) == 0 {
			return nil, errors.New(fmt.Sprintf("unable to find %s label on StatefulSet %s", config.RolloutGroupLabelKey, sts.Name))
		}

		groupReq, err := labels.NewRequirement(config.RolloutGroupLabelKey, selection.Equals, []string{rolloutGroup})
		if err != nil {
			return nil, err
		}

		sel := labels.NewSelector().Add(*groupReq)
		return a.kubeClient.AppsV1().StatefulSets(sts.Namespace).List(a.ctx, metav1.ListOptions{
			LabelSelector: sel.String(),
		})
	} else {
		return a.kubeClient.AppsV1().StatefulSets(sts.Namespace).List(a.ctx, metav1.ListOptions{
			LabelSelector: (*selector).String(),
		})
	}

}

// podsNotRunningAndReady finds the pods managed by a given StatefulSet. Each pod is inspected to see if it is ready and running. A tally of the total number of pods and the number not ready/running is returned.
// It is possible for pods to be in a state where they are not yet returned by the pod listing. These pods should be considered and are reported as unknown.
// The number of unknown pods is determined as the difference between the StatefulSets State.Replica count minus the number of pods listed.
// The given Pod is excluded from testing, but is included in the total of tested pods
func (a *k8sClients) podsNotRunningAndReady(sts *appsv1.StatefulSet, pod *corev1.Pod, matcher partitionMatcher) (*podStatusResult, error) {
	podsSelector := labels.NewSelector().Add(
		util.MustNewLabelsRequirement("name", selection.Equals, []string{sts.Spec.Template.Labels["name"]}),
	)

	list, err := a.kubeClient.CoreV1().Pods(sts.Namespace).List(a.ctx, metav1.ListOptions{
		LabelSelector: podsSelector.String(),
	})

	if err != nil {
		return nil, err
	}

	result := &podStatusResult{tested: 0, notReady: 0, unknown: 0}

	// replicas is the number of Pods created by the StatefulSet controller.
	replicas := int(sts.Status.Replicas)
	for _, pd := range list.Items {

		// we do not consider pods which are in a different partition
		if !matcher.same(&pd) {
			continue
		}

		if pod.UID != pd.UID && !util.IsPodRunningAndReady(&pd) {
			result.notReady++
		}
		
		result.tested++
	}

	// we consider the pod as not ready if there should be a given replica count but it is not yet being found in the pods query
	// note that the effect here is that we do not know which partition these other pods will be in, so we have to attribute them to this partition to be safe
	if len(list.Items) < replicas {
		result.unknown += replicas - len(list.Items)
	}

	return result, nil
}
