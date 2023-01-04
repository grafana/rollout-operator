//go:build requires_docker

package integration

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/grafana/rollout-operator/integration/k3t"
)

func TestRolloutHappyCase(t *testing.T) {
	ctx := context.Background()

	cluster := k3t.NewCluster(ctx, t, k3t.WithImages("rollout-operator:latest", "mock-service:latest"))
	api := cluster.API()

	// Create rollout operator and check it's running and ready.
	createRolloutOperator(t, ctx, api, corev1.NamespaceDefault, false)
	rolloutOperatorPod := eventuallyGetFirstPod(ctx, t, api, "name=rollout-operator")
	requireEventuallyPod(t, api, ctx, rolloutOperatorPod, expectPodPhase(corev1.PodRunning), expectReady())

	// Create mock service, and check that it is in the desired state.
	createMockServiceZone(t, ctx, api, corev1.NamespaceDefault, "mock-zone-a")
	createMockServiceZone(t, ctx, api, corev1.NamespaceDefault, "mock-zone-b")
	createMockServiceZone(t, ctx, api, corev1.NamespaceDefault, "mock-zone-c")
	requireEventuallyPod(t, api, ctx, "mock-zone-a-0", expectPodPhase(corev1.PodRunning), expectReady(), expectVersion("1"))
	requireEventuallyPod(t, api, ctx, "mock-zone-b-0", expectPodPhase(corev1.PodRunning), expectReady(), expectVersion("1"))
	requireEventuallyPod(t, api, ctx, "mock-zone-c-0", expectPodPhase(corev1.PodRunning), expectReady(), expectVersion("1"))

	// Update all mock service statefulsets.
	_, err := api.AppsV1().StatefulSets(corev1.NamespaceDefault).Update(ctx, mockServiceStatefulSet("mock-zone-a", "2", false), metav1.UpdateOptions{})
	require.NoError(t, err, "Can't update StatefulSet")
	_, err = api.AppsV1().StatefulSets(corev1.NamespaceDefault).Update(ctx, mockServiceStatefulSet("mock-zone-b", "2", false), metav1.UpdateOptions{})
	require.NoError(t, err, "Can't update StatefulSet")
	_, err = api.AppsV1().StatefulSets(corev1.NamespaceDefault).Update(ctx, mockServiceStatefulSet("mock-zone-c", "2", false), metav1.UpdateOptions{})
	require.NoError(t, err, "Can't update StatefulSet")

	// First pod should be now version 2 and not be ready, the rest should be ready yet.
	requireEventuallyPod(t, api, ctx, "mock-zone-a-0", expectNotReady(), expectVersion("2"))
	requireEventuallyPod(t, api, ctx, "mock-zone-b-0", expectReady(), expectVersion("1"))
	requireEventuallyPod(t, api, ctx, "mock-zone-c-0", expectReady(), expectVersion("1"))

	// zone-a becomes ready, zone-b should become not ready and be version 2.
	makeMockReady(t, cluster, "mock-zone-a")
	requireEventuallyPod(t, api, ctx, "mock-zone-a-0", expectReady(), expectVersion("2"))
	requireEventuallyPod(t, api, ctx, "mock-zone-b-0", expectNotReady(), expectVersion("2"))
	requireEventuallyPod(t, api, ctx, "mock-zone-c-0", expectReady(), expectVersion("1"))

	// zone-b becomes ready, zone-c should become not ready and be version 2.
	makeMockReady(t, cluster, "mock-zone-b")
	requireEventuallyPod(t, api, ctx, "mock-zone-a-0", expectReady(), expectVersion("2"))
	requireEventuallyPod(t, api, ctx, "mock-zone-b-0", expectReady(), expectVersion("2"))
	requireEventuallyPod(t, api, ctx, "mock-zone-c-0", expectNotReady(), expectVersion("2"))

	// zone-c becomes ready, final state.
	makeMockReady(t, cluster, "mock-zone-c")
	requireEventuallyPod(t, api, ctx, "mock-zone-a-0", expectReady(), expectVersion("2"))
	requireEventuallyPod(t, api, ctx, "mock-zone-b-0", expectReady(), expectVersion("2"))
	requireEventuallyPod(t, api, ctx, "mock-zone-c-0", expectReady(), expectVersion("2"))
}

func TestNoDownscale(t *testing.T) {
	ctx := context.Background()

	cluster := k3t.NewCluster(ctx, t, k3t.WithImages("rollout-operator:latest", "mock-service:latest"))
	api := cluster.API()

	requireCreateStatefulSet := func(sts *appsv1.StatefulSet) {
		_, err := api.AppsV1().StatefulSets(corev1.NamespaceDefault).Create(ctx, sts, metav1.CreateOptions{})
		require.NoError(t, err, "Can't create StatefulSet")
	}

	requireUpdateStatefulSet := func(sts *appsv1.StatefulSet) {
		_, err := api.AppsV1().StatefulSets(corev1.NamespaceDefault).Update(ctx, sts, metav1.UpdateOptions{})
		require.NoError(t, err, "Can't update StatefulSet")
	}

	// Create the webhook before the rollout-operator, as rollout-operator should update its certificate.
	_, err := api.AdmissionregistrationV1().ValidatingWebhookConfigurations().Create(ctx, noDownscaleValidatingWebhook(corev1.NamespaceDefault), metav1.CreateOptions{})
	require.NoError(t, err)

	// Create rollout operator and check it's running and ready.
	createRolloutOperator(t, ctx, api, corev1.NamespaceDefault, true)
	rolloutOperatorPod := eventuallyGetFirstPod(ctx, t, api, "name=rollout-operator")
	requireEventuallyPod(t, api, ctx, rolloutOperatorPod, expectPodPhase(corev1.PodRunning), expectReady())

	// Create the service with two replicas.
	mock := mockServiceStatefulSet("mock", "1", true)
	mock.Spec.Replicas = ptr[int32](2)
	requireCreateStatefulSet(mock)
	requireEventuallyPodCount(ctx, t, api, "name=mock", 2)

	// Scale down, we should be able as it's not labeled with grafana/no-downscale.
	mock.Spec.Replicas = ptr[int32](1)
	requireUpdateStatefulSet(mock)
	requireEventuallyPodCount(ctx, t, api, "name=mock", 1)

	// Scale up and make it have two replicas again.
	mock.ObjectMeta.Labels["grafana.com/no-downscale"] = "true"
	mock.Spec.Replicas = ptr[int32](2)
	requireUpdateStatefulSet(mock)
	requireEventuallyPodCount(ctx, t, api, "name=mock", 2)

	// Downscale, this should be rejected.
	mock.Spec.Replicas = ptr[int32](1)
	_, err = api.AppsV1().StatefulSets(corev1.NamespaceDefault).Update(ctx, mock, metav1.UpdateOptions{})
	require.Error(t, err, "Downscale should fail")
	require.ErrorContains(t, err, `downscale of mock (StatefulSet.apps) from 2 to 1 replicas is not allowed because it has the label 'grafana.com/no-downscale=true`)

	// Dry run is also rejected.
	_, err = api.AppsV1().StatefulSets(corev1.NamespaceDefault).Update(ctx, mock, metav1.UpdateOptions{DryRun: []string{metav1.DryRunAll}})
	require.Error(t, err, "Downscale should fail")
	require.ErrorContains(t, err, `downscale of mock (StatefulSet.apps) from 2 to 1 replicas is not allowed because it has the label 'grafana.com/no-downscale=true`)

	// Upscaling should still work correctly.
	mock.Spec.Replicas = ptr[int32](3)
	requireUpdateStatefulSet(mock)
}
