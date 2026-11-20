package zpdb

import (
	"context"

	"github.com/prometheus/client_golang/prometheus"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	corelisters "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/metadata"
)

// UsePodMetadata enables live metadata reads for partition-aware checks. Call before Start.
func (c *EvictionController) UsePodMetadata(client metadata.Interface) {
	c.partitionPods = &partitionPodReader{
		metadata:  client.Resource(corev1.SchemeGroupVersion.WithResource("pods")),
		pods:      c.podObserver.podLister,
		fallbacks: c.metrics.PartitionFullListFallbacks,
	}
}

type partitionPodReader struct {
	metadata  metadata.Getter
	pods      corelisters.PodLister
	fallbacks prometheus.Counter
}

// matchingPods returns the peers and the total pod count from the same live list.
// A version mismatch discards the whole zone so the caller can take a fresh snapshot.
func (r *partitionPodReader) matchingPods(ctx context.Context, namespace string, opts metav1.ListOptions, matches partitionMatcher) ([]*corev1.Pod, int, bool, error) {
	list, err := r.metadata.Namespace(namespace).List(ctx, opts)
	if apierrors.IsNotAcceptable(err) {
		return nil, 0, false, nil
	}
	if err != nil {
		return nil, 0, false, err
	}
	if list.Continue != "" {
		return nil, 0, false, nil
	}
	var peers []*corev1.Pod
	for i := range list.Items {
		item := &list.Items[i]
		if !matches(item) {
			continue
		}
		peer, err := r.pods.Pods(namespace).Get(item.Name)
		if err != nil || item.UID == "" || item.ResourceVersion == "" || peer.UID != item.UID || peer.ResourceVersion != item.ResourceVersion {
			return nil, 0, false, nil
		}
		peers = append(peers, peer)
	}
	return peers, len(list.Items), true, nil
}
