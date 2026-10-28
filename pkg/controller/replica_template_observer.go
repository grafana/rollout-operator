package controller

import (
	"context"

	"github.com/go-kit/log/level"
	autoscalingv1 "k8s.io/api/autoscaling/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/scale"
	"k8s.io/client-go/tools/cache"
)

var replicaTemplateGVR = schema.GroupVersionResource{
	Group:    "rollout-operator.grafana.com",
	Version:  "v1",
	Resource: "replicatemplates",
}

var replicaTemplateGroupKind = schema.GroupKind{
	Group: "rollout-operator.grafana.com",
	Kind:  "ReplicaTemplate",
}

// WatchReplicaTemplates enables prompt reconciliation of desired replica changes.
// Call before Init with list/watch permissions; the informer retries until the CRD is available.
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

// AllowMissingCRDs selects asynchronous ReplicaTemplate startup and late discovery recovery.
// Call before Init so normal reconciliation needs no bootstrap-specific behavior.
func (c *RolloutController) AllowMissingCRDs() {
	c.replicaTemplateStartupSynced = func() bool { return true }
	// Polling retries mirroring after transient errors even when no watch is enabled.
	c.onReplicaTemplateDiscovered = c.enqueueReconcile
	c.restMapper = replicaTemplateRESTMapper{RESTMapper: c.restMapper}
	c.scaleClient = replicaTemplateScalesGetter{ScalesGetter: c.scaleClient, dynamicClient: c.dynamicClient}
}

type replicaTemplateRESTMapper struct {
	meta.RESTMapper
}

func (m replicaTemplateRESTMapper) RESTMappings(gk schema.GroupKind, versions ...string) ([]*meta.RESTMapping, error) {
	mappings, err := m.RESTMapper.RESTMappings(gk, versions...)
	if meta.IsNoMatchError(err) && gk == replicaTemplateGroupKind {
		return []*meta.RESTMapping{{Resource: replicaTemplateGVR, GroupVersionKind: replicaTemplateGVR.GroupVersion().WithKind(gk.Kind), Scope: meta.RESTScopeNamespace}}, nil
	}
	return mappings, err
}

type replicaTemplateScalesGetter struct {
	scale.ScalesGetter
	dynamicClient dynamic.Interface
}

func (s replicaTemplateScalesGetter) Scales(namespace string) scale.ScaleInterface {
	return replicaTemplateScaleInterface{ScaleInterface: s.ScalesGetter.Scales(namespace), resource: s.dynamicClient.Resource(replicaTemplateGVR).Namespace(namespace)}
}

type replicaTemplateScaleInterface struct {
	scale.ScaleInterface
	resource dynamic.ResourceInterface
}

func (s replicaTemplateScaleInterface) Get(ctx context.Context, gr schema.GroupResource, name string, opts metav1.GetOptions) (*autoscalingv1.Scale, error) {
	result, err := s.ScaleInterface.Get(ctx, gr, name, opts)
	if err == nil || gr != replicaTemplateGVR.GroupResource() {
		return result, err
	}
	// The scale client's discovery cache may predate installation of this known CRD.
	obj, err := s.resource.Get(ctx, name, opts, "scale")
	if err != nil {
		return nil, err
	}
	result = &autoscalingv1.Scale{}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj.Object, result); err != nil {
		return nil, err
	}
	return result, nil
}
