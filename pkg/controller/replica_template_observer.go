package controller

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic/dynamicinformer"
)

var replicaTemplateGVR = schema.GroupVersionResource{
	Group:    "rollout-operator.grafana.com",
	Version:  "v1",
	Resource: "replicatemplates",
}

// WatchReplicaTemplates enables prompt reconciliation of desired replica changes.
// Call before Init, only when the CRD and list/watch permissions are available.
func (c *RolloutController) WatchReplicaTemplates() {
	c.replicaTemplatesFactory = dynamicinformer.NewFilteredDynamicSharedInformerFactory(c.dynamicClient, informerSyncInterval, c.namespace, nil)
	c.replicaTemplatesInformer = c.replicaTemplatesFactory.ForResource(replicaTemplateGVR).Informer()
}

func (c *RolloutController) onReplicaTemplateUpdated(old, new interface{}) {
	oldTemplate, oldOK := old.(*unstructured.Unstructured)
	newTemplate, newOK := new.(*unstructured.Unstructured)
	// The controller writes status.replicas itself; these updates must not trigger another reconcile.
	if oldOK && newOK && oldTemplate.GetGeneration() != newTemplate.GetGeneration() {
		c.enqueueReconcile()
	}
}
