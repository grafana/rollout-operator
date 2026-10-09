//go:build requires_docker

package integration

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func TestLeaderElectionTwoDeployments(t *testing.T) {
	ctx := context.Background()
	cluster := createKindCluster(t, "rollout-operator:latest")
	api := cluster.API()
	path := initManifestFiles(t, "webhooks-enabled")
	createRolloutOperatorDependencies(t, ctx, api, cluster.ExtAPI(), path, true)
	for _, zone := range []string{"a", "b"} {
		deployment := loadFromDisk[appsv1.Deployment](t, path+yamlDeployment, &appsv1.Deployment{})
		deployment.Name = "rollout-operator-" + zone
		deployment.Spec.Selector.MatchLabels["operator-zone"] = zone
		deployment.Spec.Template.Labels["operator-zone"] = zone
		deployment.Spec.Template.Spec.Containers[0].Image = "rollout-operator:latest"
		deployment.Spec.Template.Spec.Containers[0].ImagePullPolicy = corev1.PullNever
		_, err := api.AppsV1().Deployments(corev1.NamespaceDefault).Create(ctx, deployment, metav1.CreateOptions{})
		require.NoError(t, err)
	}
	available := func(name string) func() bool {
		return func() bool {
			deployment, err := api.AppsV1().Deployments(corev1.NamespaceDefault).Get(ctx, name, metav1.GetOptions{})
			return err == nil && deployment.Status.ObservedGeneration >= deployment.Generation && deployment.Status.UpdatedReplicas == 1 && deployment.Status.AvailableReplicas == 1
		}
	}
	leaderEndpoint := func() bool {
		slices, err := api.DiscoveryV1().EndpointSlices(corev1.NamespaceDefault).List(ctx, metav1.ListOptions{LabelSelector: "kubernetes.io/service-name=rollout-operator"})
		if err != nil {
			return false
		}
		count := 0
		for _, slice := range slices.Items {
			for _, endpoint := range slice.Endpoints {
				if endpoint.Conditions.Ready == nil || !*endpoint.Conditions.Ready {
					continue
				}
				if endpoint.TargetRef == nil {
					return false
				}
				pod, err := api.CoreV1().Pods(corev1.NamespaceDefault).Get(ctx, endpoint.TargetRef.Name, metav1.GetOptions{})
				if err != nil || pod.Labels["rollout-operator.grafana.com/leader"] != "true" {
					return false
				}
				count++
			}
		}
		return count == 1
	}
	for _, name := range []string{"rollout-operator-a", "rollout-operator-b"} {
		require.Eventually(t, available(name), 3*time.Minute, time.Second, "both Deployments must become available")
	}
	require.Eventually(t, leaderEndpoint, time.Minute, time.Second, "Service must route to only the leader")
	// Updating both Deployments exercises replacement of the standby and handoff from the leader.
	for _, name := range []string{"rollout-operator-a", "rollout-operator-b"} {
		_, err := api.AppsV1().Deployments(corev1.NamespaceDefault).Patch(ctx, name, types.MergePatchType, []byte(`{"spec":{"template":{"metadata":{"annotations":{"test-rollout":"updated"}}}}}`), metav1.PatchOptions{})
		require.NoError(t, err)
		require.Eventually(t, available(name), 3*time.Minute, time.Second, "Deployment update must finish")
		require.Eventually(t, leaderEndpoint, time.Minute, time.Second, "leader routing must recover after replacement")
	}
}
