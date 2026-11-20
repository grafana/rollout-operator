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
	partitionPods *partitionPodReader
	ctx           context.Context
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

func statefulSetOwnerReference(pod *corev1.Pod) (*metav1.OwnerReference, error) {
	owner := metav1.GetControllerOf(pod)
	if owner == nil {
		return nil, errors.New("unable to find a StatefulSet pod owner")
	} else if owner.Kind != "StatefulSet" {
		return nil, fmt.Errorf("pod owner is not a StatefulSet - %s has owner %s (%s)", pod.Name, owner.Name, owner.Kind)
	}
	return owner, nil
}

// owner resolves a validated StatefulSet owner reference against the related sets or the API.
func (a *k8sClient) owner(pod *corev1.Pod, owner *metav1.OwnerReference, related *appsv1.StatefulSetList) (*appsv1.StatefulSet, error) {
	if related != nil {
		for i := range related.Items {
			if related.Items[i].Name == owner.Name {
				return &related.Items[i], nil
			}
		}
	}

	// The ZPDB selector may exclude the owner, so keep the live lookup for that case.
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
func (a *k8sClient) podsNotRunningAndReady(sts *appsv1.StatefulSet, pod *corev1.Pod, zpdb validator) (*zoneStatusResult, error) {
	podsSelector := labels.NewSelector().Add(
		util.MustNewLabelsRequirement("name", selection.Equals, []string{sts.Spec.Template.Labels["name"]}),
	)

	opts := metav1.ListOptions{LabelSelector: podsSelector.String()}
	matcher := zpdb.considerPod()
	result := &zoneStatusResult{}
	count := func(peer *corev1.Pod) { countPeer(result, pod, zpdb, peer) }

	if a.partitionPods != nil {
		peers, total, ok, err := a.partitionPods.matchingPods(a.ctx, sts.Namespace, opts, matcher)
		if err != nil {
			return nil, err
		}
		if ok {
			for _, peer := range peers {
				count(peer)
			}
			result.unknown = missingReplicas(sts, total)
			return result, nil
		}
		a.partitionPods.fallbacks.Inc()
	}

	list, err := a.kubeClient.CoreV1().Pods(sts.Namespace).List(a.ctx, opts)
	if err != nil {
		return nil, err
	}
	for i := range list.Items {
		peer := &list.Items[i]
		if matcher(peer) {
			count(peer)
		}
	}
	result.unknown = missingReplicas(sts, len(list.Items))
	return result, nil
}

// countPeer adds a peer that the validator considers to the zone's tally. The eviction Pod itself counts as
// tested but never as not ready. The peer may be shared with the informer cache, so it must not be modified.
func countPeer(result *zoneStatusResult, pod *corev1.Pod, zpdb validator, peer *corev1.Pod) {
	result.tested++
	if pod.UID != peer.UID && !zpdb.isReady(peer) {
		result.notReady++
	}
}

// zoneStatus tallies a zone's Pods from the informer cache, as podsNotRunningAndReady does from live reads.
// The Pods are shared with the informer, so they must not be modified.
func zoneStatus(sts *appsv1.StatefulSet, pod *corev1.Pod, zpdb validator, pods []*corev1.Pod) *zoneStatusResult {
	result := &zoneStatusResult{}
	matcher := zpdb.considerPod()
	for _, peer := range pods {
		if matcher(peer) {
			countPeer(result, pod, zpdb, peer)
		}
	}
	result.unknown = missingReplicas(sts, len(pods))
	return result
}

// Missing replicas have unknown partitions and count against the requested budget.
func missingReplicas(sts *appsv1.StatefulSet, observed int) int {
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

	return max(0, replicas-observed)
}
