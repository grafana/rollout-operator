//go:build requires_docker

package integration

import (
	"context"
	"k8s.io/client-go/kubernetes"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	policyv1beta1 "k8s.io/api/policy/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	_ "k8s.io/client-go/kubernetes/scheme"

	"github.com/grafana/rollout-operator/integration/k3t"
)

func TestRolloutHappyCase(t *testing.T) {
	ctx := context.Background()

	cluster := k3t.NewCluster(ctx, t, k3t.WithImages("rollout-operator:latest", "mock-service:latest"))
	api := cluster.API()

	// Create rollout operator and check it's running and ready.
	createRolloutOperator(t, ctx, api, cluster.ExtAPI(), corev1.NamespaceDefault, false)
	rolloutOperatorPod := eventuallyGetFirstPod(ctx, t, api, "name=rollout-operator")
	requireEventuallyPod(t, api, ctx, rolloutOperatorPod, expectPodPhase(corev1.PodRunning), expectReady())

	// Create mock service, and check that it is in the desired state.
	createMockServiceZone(t, ctx, api, corev1.NamespaceDefault, "mock-zone-a", int32(1))
	createMockServiceZone(t, ctx, api, corev1.NamespaceDefault, "mock-zone-b", int32(1))
	createMockServiceZone(t, ctx, api, corev1.NamespaceDefault, "mock-zone-c", int32(1))
	requireEventuallyPod(t, api, ctx, "mock-zone-a-0", expectPodPhase(corev1.PodRunning), expectReady(), expectVersion("1"))
	requireEventuallyPod(t, api, ctx, "mock-zone-b-0", expectPodPhase(corev1.PodRunning), expectReady(), expectVersion("1"))
	requireEventuallyPod(t, api, ctx, "mock-zone-c-0", expectPodPhase(corev1.PodRunning), expectReady(), expectVersion("1"))

	// Update all mock service statefulsets.
	_, err := api.AppsV1().StatefulSets(corev1.NamespaceDefault).Update(ctx, mockServiceStatefulSet("mock-zone-a", "2", false, int32(1)), metav1.UpdateOptions{})
	require.NoError(t, err, "Can't update StatefulSet")
	_, err = api.AppsV1().StatefulSets(corev1.NamespaceDefault).Update(ctx, mockServiceStatefulSet("mock-zone-b", "2", false, int32(1)), metav1.UpdateOptions{})
	require.NoError(t, err, "Can't update StatefulSet")
	_, err = api.AppsV1().StatefulSets(corev1.NamespaceDefault).Update(ctx, mockServiceStatefulSet("mock-zone-c", "2", false, int32(1)), metav1.UpdateOptions{})
	require.NoError(t, err, "Can't update StatefulSet")

	// First pod should be now version 2 and not be ready, the rest should be ready yet.
	requireEventuallyPod(t, api, ctx, "mock-zone-a-0", expectNotReady(), expectVersion("2"))
	requireEventuallyPod(t, api, ctx, "mock-zone-b-0", expectReady(), expectVersion("1"))
	requireEventuallyPod(t, api, ctx, "mock-zone-c-0", expectReady(), expectVersion("1"))

	// zone-a becomes ready, zone-b should become not ready and be version 2.
	time.Sleep(30 * time.Second)
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

func awaitCABundleAssignment(webhookCnt int, ctx context.Context, api *kubernetes.Clientset) func() bool {
	return func() bool {
		list, err := api.AdmissionregistrationV1().ValidatingWebhookConfigurations().List(ctx, metav1.ListOptions{})
		if err != nil {
			return false
		}
		if len(list.Items) != webhookCnt {
			return false
		}

		success := true
		for _, webhook := range list.Items {
			if webhook.Webhooks[0].ClientConfig.CABundle == nil {
				success = false
			}
		}

		return success
	}
}

// TestWebHookInformer validates that we can add validating webhooks before or after the rollout-operator has started, and they will have their CABundle decorated
func TestWebHookInformer(t *testing.T) {
	ctx := context.Background()

	cluster := k3t.NewCluster(ctx, t, k3t.WithImages("rollout-operator:latest", "mock-service:latest"))
	api := cluster.API()

	{
		t.Log("Add a webhook before the rollout-operator is created")
		wh, err := api.AdmissionregistrationV1().ValidatingWebhookConfigurations().Create(ctx, noDownscaleValidatingWebhook(corev1.NamespaceDefault), metav1.CreateOptions{})
		require.NoError(t, err)
		require.Nil(t, wh.Webhooks[0].ClientConfig.CABundle)
	}

	{
		t.Log("Add a webhook before the rollout-operator is created")
		createRolloutOperator(t, ctx, api, cluster.ExtAPI(), corev1.NamespaceDefault, true)

		t.Log("Await CABundle assignment")
		require.Eventually(t, awaitCABundleAssignment(1, ctx, api), time.Second*30, time.Millisecond*10, "New webhooks have CABundle added")
	}

	{
		t.Log("Add a webhook after the rollout-operator has created")
		wh, err := api.AdmissionregistrationV1().ValidatingWebhookConfigurations().Create(ctx, zpdbValidatingWebhook(corev1.NamespaceDefault), metav1.CreateOptions{})
		require.NoError(t, err)
		require.Nil(t, wh.Webhooks[0].ClientConfig.CABundle)

		t.Log("Await CABundle assignment")
		require.Eventually(t, awaitCABundleAssignment(2, ctx, api), time.Second*30, time.Millisecond*10, "New webhooks have CABundle added")
	}

	{
		t.Log("Add another webhooks")
		wh, err := api.AdmissionregistrationV1().ValidatingWebhookConfigurations().Create(ctx, podEvictionValidatingWebhook(corev1.NamespaceDefault), metav1.CreateOptions{})
		require.NoError(t, err)
		require.Nil(t, wh.Webhooks[0].ClientConfig.CABundle)

		t.Log("Await CABundle assignment")
		require.Eventually(t, awaitCABundleAssignment(3, ctx, api), time.Second*30, time.Millisecond*10, "New webhooks have CABundle added")
	}
}

func TestZoneAwarePodDisruptionBudget(t *testing.T) {
	ctx := context.Background()

	cluster := k3t.NewCluster(ctx, t, k3t.WithImages("rollout-operator:latest", "mock-service:latest"))
	api := cluster.API()

	_, err := api.AdmissionregistrationV1().ValidatingWebhookConfigurations().Create(ctx, zpdbValidatingWebhook(corev1.NamespaceDefault), metav1.CreateOptions{})
	require.NoError(t, err)

	_, err = api.AdmissionregistrationV1().ValidatingWebhookConfigurations().Create(ctx, podEvictionValidatingWebhook(corev1.NamespaceDefault), metav1.CreateOptions{})
	require.NoError(t, err)

	{
		t.Log("Create rollout operator and check it's running and ready.")
		createRolloutOperator(t, ctx, api, cluster.ExtAPI(), corev1.NamespaceDefault, true)
		rolloutOperatorPod := eventuallyGetFirstPod(ctx, t, api, "name=rollout-operator")
		requireEventuallyPod(t, api, ctx, rolloutOperatorPod, expectPodPhase(corev1.PodRunning), expectReady())
	}

	{
		t.Log("Try to create an invalid zpdb configuration.")
		zpdb := zoneAwarePodDisruptionBudget(corev1.NamespaceDefault, "ingester-zpdb", "mock", -1)
		_, err = cluster.DynK().Resource(zoneAwarePodDisruptionBudgetSchema()).Namespace(corev1.NamespaceDefault).Create(ctx, zpdb, metav1.CreateOptions{})
		require.Error(t, err)
	}

	{
		t.Log("Create a valid zpdb configuration.")
		zpdb := zoneAwarePodDisruptionBudget(corev1.NamespaceDefault, "ingester-zpdb", "mock", 1)
		_, err = cluster.DynK().Resource(zoneAwarePodDisruptionBudgetSchema()).Namespace(corev1.NamespaceDefault).Create(ctx, zpdb, metav1.CreateOptions{})
		require.NoError(t, err)
	}

	// Create mock service, and check that it is in the desired state.
	createMockServiceZone(t, ctx, api, corev1.NamespaceDefault, "mock-zone-a", int32(3))
	createMockServiceZone(t, ctx, api, corev1.NamespaceDefault, "mock-zone-b", int32(3))
	createMockServiceZone(t, ctx, api, corev1.NamespaceDefault, "mock-zone-c", int32(3))
	requireEventuallyPod(t, api, ctx, "mock-zone-a-0", expectPodPhase(corev1.PodRunning), expectReady(), expectVersion("1"))
	requireEventuallyPod(t, api, ctx, "mock-zone-b-0", expectPodPhase(corev1.PodRunning), expectReady(), expectVersion("1"))
	requireEventuallyPod(t, api, ctx, "mock-zone-c-0", expectPodPhase(corev1.PodRunning), expectReady(), expectVersion("1"))
	requireEventuallyPod(t, api, ctx, "mock-zone-a-1", expectPodPhase(corev1.PodRunning), expectReady(), expectVersion("1"))
	requireEventuallyPod(t, api, ctx, "mock-zone-b-1", expectPodPhase(corev1.PodRunning), expectReady(), expectVersion("1"))
	requireEventuallyPod(t, api, ctx, "mock-zone-c-1", expectPodPhase(corev1.PodRunning), expectReady(), expectVersion("1"))
	requireEventuallyPod(t, api, ctx, "mock-zone-a-2", expectPodPhase(corev1.PodRunning), expectReady(), expectVersion("1"))
	requireEventuallyPod(t, api, ctx, "mock-zone-b-2", expectPodPhase(corev1.PodRunning), expectReady(), expectVersion("1"))
	requireEventuallyPod(t, api, ctx, "mock-zone-c-2", expectPodPhase(corev1.PodRunning), expectReady(), expectVersion("1"))

	nodeList, err := api.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	for _, node := range nodeList.Items {
		t.Logf("Cordon node %s", node.Name)
		node.Spec.Unschedulable = true
		_, err = api.CoreV1().Nodes().Update(ctx, &node, metav1.UpdateOptions{})
		require.NoError(t, err)

		ev1 := &policyv1beta1.Eviction{ObjectMeta: metav1.ObjectMeta{Name: "mock-zone-a-0", Namespace: corev1.NamespaceDefault}}
		require.NoError(t, api.PolicyV1beta1().Evictions(corev1.NamespaceDefault).Evict(ctx, ev1))

		ev2 := &policyv1beta1.Eviction{ObjectMeta: metav1.ObjectMeta{Name: "mock-zone-b-0", Namespace: corev1.NamespaceDefault}}
		require.ErrorContainsf(t, api.PolicyV1beta1().Evictions(corev1.NamespaceDefault).Evict(ctx, ev2), "denied the request: 1 pod not ready under mock-zone-a", "Eviction denied")
	}
}

func TestNoDownscale_CanDownscaleUnrelatedResource(t *testing.T) {
	ctx := context.Background()

	cluster := k3t.NewCluster(ctx, t, k3t.WithImages("rollout-operator:latest", "mock-service:latest"))
	api := cluster.API()

	{
		t.Log("Create the webhook before the rollout-operator, as rollout-operator should update its certificate.")
		_, err := api.AdmissionregistrationV1().ValidatingWebhookConfigurations().Create(ctx, noDownscaleValidatingWebhook(corev1.NamespaceDefault), metav1.CreateOptions{})
		require.NoError(t, err)
	}

	{
		t.Log("Create rollout operator and check it's running and ready.")
		createRolloutOperator(t, ctx, api, cluster.ExtAPI(), corev1.NamespaceDefault, true)
		rolloutOperatorPod := eventuallyGetFirstPod(ctx, t, api, "name=rollout-operator")
		requireEventuallyPod(t, api, ctx, rolloutOperatorPod, expectPodPhase(corev1.PodRunning), expectReady())
	}

	mock := mockServiceStatefulSet("mock", "1", true, int32(1))
	{
		t.Log("Create the service with two replicas.")
		mock.Spec.Replicas = ptr[int32](2)
		requireCreateStatefulSet(ctx, t, api, mock)
		requireEventuallyPodCount(ctx, t, api, "name=mock", 2)
	}

	{
		t.Log("Scale down, we should be able as it's not labeled with grafana/no-downscale.")
		mock.Spec.Replicas = ptr[int32](1)
		requireUpdateStatefulSet(ctx, t, api, mock)
		requireEventuallyPodCount(ctx, t, api, "name=mock", 1)
	}

	{
		t.Log("Scale up and make it have two replicas again.")
		mock.Spec.Replicas = ptr[int32](2)
		requireUpdateStatefulSet(ctx, t, api, mock)
		requireEventuallyPodCount(ctx, t, api, "name=mock", 2)
	}

	{
		t.Log("Scale down using /scale subresource, we should be able as it's not labeled with grafana/no-downscale.")
		err := getAndUpdateStatefulSetScale(ctx, t, api, "mock", 1, false)
		require.NoError(t, err)
		requireEventuallyPodCount(ctx, t, api, "name=mock", 1)
	}
}

func TestNoDownscale_DownscaleUpdatingStatefulSet(t *testing.T) {
	ctx := context.Background()

	cluster := k3t.NewCluster(ctx, t, k3t.WithImages("rollout-operator:latest", "mock-service:latest"))
	api := cluster.API()

	{
		t.Log("Create the webhook before the rollout-operator, as rollout-operator should update its certificate.")
		_, err := api.AdmissionregistrationV1().ValidatingWebhookConfigurations().Create(ctx, noDownscaleValidatingWebhook(corev1.NamespaceDefault), metav1.CreateOptions{})
		require.NoError(t, err)
	}

	{
		t.Log("Create rollout operator and check it's running and ready.")
		createRolloutOperator(t, ctx, api, cluster.ExtAPI(), corev1.NamespaceDefault, true)
		rolloutOperatorPod := eventuallyGetFirstPod(ctx, t, api, "name=rollout-operator")
		requireEventuallyPod(t, api, ctx, rolloutOperatorPod, expectPodPhase(corev1.PodRunning), expectReady())
	}

	mock := mockServiceStatefulSet("mock", "1", true, int32(1))
	{
		t.Log("Create the service with two replicas.")
		mock.Labels["grafana.com/no-downscale"] = "true"
		mock.Spec.Replicas = ptr[int32](2)
		requireCreateStatefulSet(ctx, t, api, mock)
		requireEventuallyPodCount(ctx, t, api, "name=mock", 2)
	}

	{
		t.Log("Downscale, this should be rejected.")
		mock.Spec.Replicas = ptr[int32](1)
		_, err := api.AppsV1().StatefulSets(corev1.NamespaceDefault).Update(ctx, mock, metav1.UpdateOptions{})
		require.Error(t, err, "Downscale should fail")
		require.ErrorContains(t, err, `downscale of statefulsets/mock in default from 2 to 1 replicas is not allowed because it has the label 'grafana.com/no-downscale=true'`)
	}

	{
		t.Log("Dry run is also rejected.")
		_, err := api.AppsV1().StatefulSets(corev1.NamespaceDefault).Update(ctx, mock, metav1.UpdateOptions{DryRun: []string{metav1.DryRunAll}})
		require.Error(t, err, "Downscale should fail")
		require.ErrorContains(t, err, `downscale of statefulsets/mock in default from 2 to 1 replicas is not allowed because it has the label 'grafana.com/no-downscale=true'`)
	}

	{
		t.Log("Upscaling should still work correctly.")
		mock.Spec.Replicas = ptr[int32](3)
		requireUpdateStatefulSet(ctx, t, api, mock)
	}
}

func TestNoDownscale_UpdatingScaleSubresource(t *testing.T) {
	ctx := context.Background()

	cluster := k3t.NewCluster(ctx, t, k3t.WithImages("rollout-operator:latest", "mock-service:latest"))
	api := cluster.API()

	{
		t.Log("Create the webhook before the rollout-operator, as rollout-operator should update its certificate.")
		_, err := api.AdmissionregistrationV1().ValidatingWebhookConfigurations().Create(ctx, noDownscaleValidatingWebhook(corev1.NamespaceDefault), metav1.CreateOptions{})
		require.NoError(t, err)
	}

	{
		t.Log("Create rollout operator and check it's running and ready.")
		createRolloutOperator(t, ctx, api, cluster.ExtAPI(), corev1.NamespaceDefault, true)
		rolloutOperatorPod := eventuallyGetFirstPod(ctx, t, api, "name=rollout-operator")
		requireEventuallyPod(t, api, ctx, rolloutOperatorPod, expectPodPhase(corev1.PodRunning), expectReady())
	}

	mock := mockServiceStatefulSet("mock", "1", true, int32(1))
	mock.Labels["grafana.com/no-downscale"] = "true"
	{
		t.Log("Create the service with two replicas.")
		mock.Spec.Replicas = ptr[int32](2)
		requireCreateStatefulSet(ctx, t, api, mock)
		requireEventuallyPodCount(ctx, t, api, "name=mock", 2)
	}

	{
		t.Log("Downscale using /scale subresource update, this should be rejected.")
		err := getAndUpdateStatefulSetScale(ctx, t, api, "mock", 1, false)
		require.Error(t, err, "Downscale should fail")
		require.ErrorContains(t, err, `downscale of statefulsets/mock in default from 2 to 1 replicas is not allowed because it has the label 'grafana.com/no-downscale=true'`)
	}

	{
		t.Log("Dry run is also rejected.")
		err := getAndUpdateStatefulSetScale(ctx, t, api, "mock", 1, true)
		require.Error(t, err, "Downscale should fail")
		require.ErrorContains(t, err, `downscale of statefulsets/mock in default from 2 to 1 replicas is not allowed because it has the label 'grafana.com/no-downscale=true'`)
	}

	{
		t.Log("Upscaling should still work correctly.")
		err := getAndUpdateStatefulSetScale(ctx, t, api, "mock", 3, false)
		require.NoError(t, err)
		requireEventuallyPodCount(ctx, t, api, "name=mock", 3)
	}
}
