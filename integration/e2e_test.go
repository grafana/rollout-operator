//go:build requires_docker

package integration

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/grafana/rollout-operator/integration/k3t"
)

func TestRolloutHappyCase(t *testing.T) {
	ctx := context.Background()

	cluster := k3t.NewCluster(ctx, t, k3t.WithImages("rollout-operator:latest", "mock-service:latest"))
	api := cluster.API()

	// Create rollout operator and check it's running and ready.
	createRolloutOperator(t, ctx, api)
	rolloutOperatorPod := eventuallyGetFirstPod(ctx, t, api, "name=rollout-operator")
	requireEventuallyPod(t, api, ctx, rolloutOperatorPod, expectPodPhase(corev1.PodRunning))
	requireEventuallyPod(t, api, ctx, rolloutOperatorPod, expectReady())

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
