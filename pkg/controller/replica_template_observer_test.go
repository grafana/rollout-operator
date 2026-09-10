package controller

import (
	"context"
	"testing"
	"time"

	"github.com/go-kit/log"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
)

func TestReplicaTemplateUpdateFiltersStatus(t *testing.T) {
	old := &unstructured.Unstructured{Object: map[string]interface{}{
		"metadata": map[string]interface{}{"generation": int64(1)},
		"spec":     map[string]interface{}{"replicas": int64(1)},
	}}
	for _, changeSpec := range []bool{false, true} {
		t.Run(map[bool]string{false: "status", true: "spec"}[changeSpec], func(t *testing.T) {
			c := &RolloutController{}
			updated := old.DeepCopy()
			if changeSpec {
				updated.SetGeneration(2)
				require.NoError(t, unstructured.SetNestedField(updated.Object, int64(5), "spec", "replicas"))
			} else {
				require.NoError(t, unstructured.SetNestedField(updated.Object, int64(1), "status", "replicas"))
			}
			c.onReplicaTemplateUpdated(old, updated)
			require.Equal(t, changeSpec, c.shouldReconcile.Load())
		})
	}
}

func TestReplicaTemplateWatchEnqueuesReconcile(t *testing.T) {
	kube := fake.NewSimpleClientset()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{replicaTemplateGVR: "ReplicaTemplateList"})
	c := NewRolloutController(kube, nil, nil, dyn, testClusterDomain, testNamespace, NewPodInformerFactory(kube, testNamespace), nil, time.Second, prometheus.NewRegistry(), log.NewNopLogger(), nil)
	c.WatchReplicaTemplates()
	t.Cleanup(c.Stop)
	require.NoError(t, c.Init())
	c.shouldReconcile.Store(false)
	resource := dyn.Resource(replicaTemplateGVR).Namespace(testNamespace)
	obj, err := resource.Create(context.Background(), &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "rollout-operator.grafana.com/v1", "kind": "ReplicaTemplate",
		"metadata": map[string]interface{}{"name": "ingester", "namespace": testNamespace, "generation": int64(1)},
		"spec":     map[string]interface{}{"replicas": int64(1)},
	}}, metav1.CreateOptions{})
	require.NoError(t, err)
	require.Eventually(t, c.shouldReconcile.Load, 5*time.Second, 10*time.Millisecond)
	c.shouldReconcile.Store(false)
	obj.SetGeneration(2)
	require.NoError(t, unstructured.SetNestedField(obj.Object, int64(5), "spec", "replicas"))
	_, err = resource.Update(context.Background(), obj, metav1.UpdateOptions{})
	require.NoError(t, err)
	require.Eventually(t, c.shouldReconcile.Load, 5*time.Second, 10*time.Millisecond)
	c.shouldReconcile.Store(false)
	require.NoError(t, resource.Delete(context.Background(), obj.GetName(), metav1.DeleteOptions{}))
	require.Eventually(t, c.shouldReconcile.Load, 5*time.Second, 10*time.Millisecond)
	for _, action := range dyn.Actions() {
		if action.GetVerb() == "list" || action.GetVerb() == "watch" {
			require.Equal(t, testNamespace, action.GetNamespace())
		}
	}
}
