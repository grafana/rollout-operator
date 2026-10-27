package controller

import (
	"context"

	"github.com/go-kit/log/level"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/tools/cache"
)

var replicaTemplateGVR = schema.GroupVersionResource{
	Group:    "rollout-operator.grafana.com",
	Version:  "v1",
	Resource: "replicatemplates",
}

// WatchReplicaTemplates enables prompt reconciliation of desired replica changes.
// Call before Init, only when the CRD and list/watch permissions are available.
func (c *RolloutController) WatchReplicaTemplates() {
	resource := c.dynamicClient.Resource(replicaTemplateGVR).Namespace(c.namespace)
	c.replicaTemplatesInformer = cache.NewSharedIndexInformer(cache.ToListWatcherWithWatchListSemantics(&cache.ListWatch{
		ListWithContextFunc: func(ctx context.Context, options metav1.ListOptions) (runtime.Object, error) {
			list, err := resource.List(ctx, options)
			if err != nil {
				c.setReplicaTemplateWatchHealthy(false)
			}
			return list, err
		},
		WatchFuncWithContext: func(ctx context.Context, options metav1.ListOptions) (watch.Interface, error) {
			w, err := resource.Watch(ctx, options)
			c.setReplicaTemplateWatchHealthy(err == nil)
			return w, err
		},
	}, c.dynamicClient), &unstructured.Unstructured{}, informerSyncInterval, cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc})
	// Watch errors after initial sync must also make the readiness alert fire.
	_ = c.replicaTemplatesInformer.SetWatchErrorHandlerWithContext(func(ctx context.Context, _ *cache.Reflector, err error) {
		c.setReplicaTemplateWatchHealthy(false)
		level.Warn(c.logger).Log("msg", "ReplicaTemplate watch failed", "err", err)
	})
}

func (c *RolloutController) onReplicaTemplateUpdated(old, new interface{}) {
	oldTemplate, oldOK := old.(*unstructured.Unstructured)
	newTemplate, newOK := new.(*unstructured.Unstructured)
	// The controller writes status.replicas itself; these updates must not trigger another reconcile.
	if oldOK && newOK && oldTemplate.GetGeneration() != newTemplate.GetGeneration() {
		c.enqueueReconcile()
	}
}

func (c *RolloutController) setReplicaTemplateWatchHealthy(healthy bool) {
	c.replicaTemplateObserverMu.Lock()
	defer c.replicaTemplateObserverMu.Unlock()
	c.replicaTemplateWatchHealthy = healthy
	c.updateReplicaTemplateReadinessLocked()
}

func (c *RolloutController) updateReplicaTemplateReadinessLocked() {
	ready := c.replicaTemplateAvailable
	if c.replicaTemplatesInformer != nil {
		ready = ready && c.replicaTemplateWatchHealthy && c.replicaTemplatesInformer.HasSynced()
	}
	if ready {
		c.replicaTemplateReady.Set(1)
	} else {
		c.replicaTemplateReady.Set(0)
	}
}
