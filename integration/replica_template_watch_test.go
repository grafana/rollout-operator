//go:build requires_docker

package integration

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	"github.com/grafana/rollout-operator/pkg/config"
)

func TestReplicaTemplateWatchScalesStatefulSet(t *testing.T) {
	ctx := context.Background()
	cluster := createKindCluster(t, "rollout-operator:latest", "mock-service:latest")
	api := cluster.API()
	path := initManifestFiles(t, "replica-template-watch")
	createRolloutOperator(t, ctx, api, cluster.ExtAPI(), path, true)
	pod := eventuallyGetFirstPod(ctx, t, api, "name=rollout-operator")
	requireEventuallyPod(t, api, ctx, pod, expectPodPhase(corev1.PodRunning), expectReady())
	resource := cluster.DynK().Resource(schema.GroupVersionResource{Group: "rollout-operator.grafana.com", Version: "v1", Resource: "replicatemplates"}).Namespace(corev1.NamespaceDefault)
	_, err := resource.Create(ctx, &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "rollout-operator.grafana.com/v1", "kind": "ReplicaTemplate",
		"metadata": map[string]interface{}{"name": "mock-zone-a"},
		"spec":     map[string]interface{}{"replicas": int64(0), "labelSelector": "name=mock-zone-a"},
	}}, metav1.CreateOptions{})
	require.NoError(t, err)
	sts := mockServiceStatefulSet("mock-zone-a", "1", true, 0)
	sts.Annotations = map[string]string{
		config.RolloutMirrorReplicasFromResourceNameAnnotationKey:       "mock-zone-a",
		config.RolloutMirrorReplicasFromResourceKindAnnotationKey:       "ReplicaTemplate",
		config.RolloutMirrorReplicasFromResourceAPIVersionAnnotationKey: "rollout-operator.grafana.com/v1",
	}
	requireCreateStatefulSet(ctx, t, api, sts)
	// Let creation events drain so only the ReplicaTemplate change can trigger the scale-up.
	require.Never(t, func() bool {
		current, err := api.AppsV1().StatefulSets(corev1.NamespaceDefault).Get(ctx, sts.Name, metav1.GetOptions{})
		require.NoError(t, err)
		return *current.Spec.Replicas != 0
	}, 15*time.Second, time.Second)
	_, err = resource.Patch(ctx, "mock-zone-a", types.MergePatchType, []byte(`{"spec":{"replicas":1}}`), metav1.PatchOptions{}, "scale")
	require.NoError(t, err)
	// A missing watch would leave replicas at zero until the five-minute informer resync.
	require.Eventually(t, func() bool {
		current, err := api.AppsV1().StatefulSets(corev1.NamespaceDefault).Get(ctx, sts.Name, metav1.GetOptions{})
		require.NoError(t, err)
		return *current.Spec.Replicas == 1
	}, 30*time.Second, 200*time.Millisecond)
	requireEventuallyPod(t, api, ctx, "mock-zone-a-0", expectPodPhase(corev1.PodRunning), expectReady())
}
