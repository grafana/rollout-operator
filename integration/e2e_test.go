//go:build requires_docker

package integration

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	policyv1beta1 "k8s.io/api/policy/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	_ "k8s.io/client-go/kubernetes/scheme"

	"github.com/grafana/rollout-operator/integration/k3t"
	"github.com/grafana/rollout-operator/pkg/util"
)

func TestRolloutHappyCase(t *testing.T) {
	ctx := context.Background()

	cluster := k3t.NewCluster(ctx, t, k3t.WithImages("rollout-operator:latest", "mock-service:latest"))
	api := cluster.API()

	path := initManifestFiles(t, "webhooks-not-enabled")

	// Create rollout operator and check it's running and ready.
	createRolloutOperator(t, ctx, api, cluster.ExtAPI(), path, false)
	rolloutOperatorPod := eventuallyGetFirstPod(ctx, t, api, "name=rollout-operator")
	requireEventuallyPod(t, api, ctx, rolloutOperatorPod, expectPodPhase(corev1.PodRunning), expectReady())

	// Create mock service, and check that it is in the desired state.
	createMockServiceZone(t, ctx, api, corev1.NamespaceDefault, "mock-zone-a", 1)
	createMockServiceZone(t, ctx, api, corev1.NamespaceDefault, "mock-zone-b", 1)
	createMockServiceZone(t, ctx, api, corev1.NamespaceDefault, "mock-zone-c", 1)
	requireEventuallyPod(t, api, ctx, "mock-zone-a-0", expectPodPhase(corev1.PodRunning), expectReady(), expectVersion("1"))
	requireEventuallyPod(t, api, ctx, "mock-zone-b-0", expectPodPhase(corev1.PodRunning), expectReady(), expectVersion("1"))
	requireEventuallyPod(t, api, ctx, "mock-zone-c-0", expectPodPhase(corev1.PodRunning), expectReady(), expectVersion("1"))

	// Update all mock service statefulsets.
	_, err := api.AppsV1().StatefulSets(corev1.NamespaceDefault).Update(ctx, mockServiceStatefulSet("mock-zone-a", "2", false, 1), metav1.UpdateOptions{})
	require.NoError(t, err, "Can't update StatefulSet")
	_, err = api.AppsV1().StatefulSets(corev1.NamespaceDefault).Update(ctx, mockServiceStatefulSet("mock-zone-b", "2", false, 1), metav1.UpdateOptions{})
	require.NoError(t, err, "Can't update StatefulSet")
	_, err = api.AppsV1().StatefulSets(corev1.NamespaceDefault).Update(ctx, mockServiceStatefulSet("mock-zone-c", "2", false, 1), metav1.UpdateOptions{})
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

// TestRolloutHappyCaseWithZpdb performs pod updates via the rolling update controller and uses the zpdb to determine if the pod delete is safe or not
func TestRolloutHappyCaseWithZpdb(t *testing.T) {
	ctx := context.Background()

	cluster := k3t.NewCluster(ctx, t, k3t.WithImages("rollout-operator:latest", "mock-service:latest"))
	api := cluster.API()

	path := initManifestFiles(t, "max-unavailable-1")

	{
		t.Log("Create rollout operator and check it's running and ready.")
		createRolloutOperator(t, ctx, api, cluster.ExtAPI(), path, true)

		_ = createValidatingWebhookConfiguration(t, api, ctx, path+yamlWebhookZpdbValidation)
		_ = createValidatingWebhookConfiguration(t, api, ctx, path+yamlWebhookPodEviction)

		rolloutOperatorPod := eventuallyGetFirstPod(ctx, t, api, "name=rollout-operator")
		requireEventuallyPod(t, api, ctx, rolloutOperatorPod, expectPodPhase(corev1.PodRunning), expectReady())

		t.Log("Await CABundle assignment")
		require.Eventually(t, awaitCABundleAssignment(2, ctx, api), time.Second*30, time.Millisecond*10, "New webhooks have CABundle added")
	}

	{
		t.Log("Create a valid zpdb configuration.")
		awaitZoneAwarePodDisruptionBudgetCreation(t, ctx, cluster, path+yamlZpdbConfig)
	}

	// Create mock service, and check that it is in the desired state.
	createMockServiceZone(t, ctx, api, corev1.NamespaceDefault, "mock-zone-a", 1)
	createMockServiceZone(t, ctx, api, corev1.NamespaceDefault, "mock-zone-b", 1)
	createMockServiceZone(t, ctx, api, corev1.NamespaceDefault, "mock-zone-c", 1)
	requireEventuallyPod(t, api, ctx, "mock-zone-a-0", expectPodPhase(corev1.PodRunning), expectReady(), expectVersion("1"))
	requireEventuallyPod(t, api, ctx, "mock-zone-b-0", expectPodPhase(corev1.PodRunning), expectReady(), expectVersion("1"))
	requireEventuallyPod(t, api, ctx, "mock-zone-c-0", expectPodPhase(corev1.PodRunning), expectReady(), expectVersion("1"))

	// Update all mock service statefulsets.
	_, err := api.AppsV1().StatefulSets(corev1.NamespaceDefault).Update(ctx, mockServiceStatefulSet("mock-zone-a", "2", false, 1), metav1.UpdateOptions{})
	require.NoError(t, err, "Can't update StatefulSet")
	_, err = api.AppsV1().StatefulSets(corev1.NamespaceDefault).Update(ctx, mockServiceStatefulSet("mock-zone-b", "2", false, 1), metav1.UpdateOptions{})
	require.NoError(t, err, "Can't update StatefulSet")
	_, err = api.AppsV1().StatefulSets(corev1.NamespaceDefault).Update(ctx, mockServiceStatefulSet("mock-zone-c", "2", false, 1), metav1.UpdateOptions{})
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

func awaitCABundleAssignment(webhookCnt int, ctx context.Context, api *kubernetes.Clientset) func() bool {
	return func() bool {

		selector := metav1.LabelSelector{MatchLabels: map[string]string{
			"grafana.com/inject-rollout-operator-ca": "true",
			"grafana.com/namespace":                  corev1.NamespaceDefault,
		}}

		labelSelector, err := metav1.LabelSelectorAsSelector(&selector)
		if err != nil {
			return false
		}

		list, err := api.AdmissionregistrationV1().ValidatingWebhookConfigurations().List(ctx, metav1.ListOptions{
			LabelSelector: labelSelector.String(),
		})
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

	path := initManifestFiles(t, "webhooks-enabled")

	{
		t.Log("Add a webhook before the rollout-operator is created")
		wh := createValidatingWebhookConfiguration(t, api, ctx, path+yamlWebhookNoDownscale)
		require.Nil(t, wh.Webhooks[0].ClientConfig.CABundle)
	}

	{
		t.Log("Starting rollout-operator with existing webhook")
		createRolloutOperator(t, ctx, api, cluster.ExtAPI(), path, true)

		t.Log("Await CABundle assignment")
		require.Eventually(t, awaitCABundleAssignment(1, ctx, api), time.Second*60, time.Millisecond*10, "New webhooks have CABundle added")
	}

	{
		t.Log("Add a webhook after the rollout-operator has created")
		wh := createValidatingWebhookConfiguration(t, api, ctx, path+yamlWebhookZpdbValidation)
		require.Nil(t, wh.Webhooks[0].ClientConfig.CABundle)

		t.Log("Await CABundle assignment")
		require.Eventually(t, awaitCABundleAssignment(2, ctx, api), time.Second*30, time.Millisecond*10, "New webhooks have CABundle added")
	}

	{
		t.Log("Add another webhook")
		wh := createValidatingWebhookConfiguration(t, api, ctx, path+yamlWebhookPodEviction)
		require.Nil(t, wh.Webhooks[0].ClientConfig.CABundle)

		t.Log("Await CABundle assignment")
		require.Eventually(t, awaitCABundleAssignment(3, ctx, api), time.Second*30, time.Millisecond*10, "New webhooks have CABundle added")

		wh, err := api.AdmissionregistrationV1().ValidatingWebhookConfigurations().Get(ctx, wh.GetName(), metav1.GetOptions{})
		require.NoError(t, err)

		t.Log("Update a webhook")
		wh.Webhooks[0].ClientConfig.CABundle = nil
		data, err := json.Marshal(wh)
		require.NoError(t, err)
		wh, err = api.AdmissionregistrationV1().ValidatingWebhookConfigurations().Patch(context.Background(), wh.GetName(), types.MergePatchType, data, metav1.PatchOptions{})
		require.NoError(t, err)
		require.Nil(t, wh.Webhooks[0].ClientConfig.CABundle)

		t.Log("Await CABundle assignment after update")
		require.Eventually(t, awaitCABundleAssignment(3, ctx, api), time.Second*30, time.Millisecond*10, "New webhooks have CABundle added")
	}
}

func TestZoneAwarePodDisruptionBudgetMaxUnavailableEq1(t *testing.T) {
	ctx := context.Background()

	cluster := k3t.NewCluster(ctx, t, k3t.WithImages("rollout-operator:latest", "mock-service:latest"))
	api := cluster.API()

	path := initManifestFiles(t, "max-unavailable-1")

	{
		t.Log("Create rollout operator and check it's running and ready.")
		createRolloutOperator(t, ctx, api, cluster.ExtAPI(), path, true)

		_ = createValidatingWebhookConfiguration(t, api, ctx, path+yamlWebhookZpdbValidation)
		_ = createValidatingWebhookConfiguration(t, api, ctx, path+yamlWebhookPodEviction)

		rolloutOperatorPod := eventuallyGetFirstPod(ctx, t, api, "name=rollout-operator")
		requireEventuallyPod(t, api, ctx, rolloutOperatorPod, expectPodPhase(corev1.PodRunning), expectReady())

		t.Log("Await CABundle assignment")
		require.Eventually(t, awaitCABundleAssignment(2, ctx, api), time.Second*30, time.Millisecond*10, "New webhooks have CABundle added")
	}

	{
		t.Log("Try to create an invalid zpdb configuration.")
		zpdb := zoneAwarePodDisruptionBudget(corev1.NamespaceDefault, "ingester-zpdb", "mock", -1)
		_, err := cluster.DynK().Resource(zoneAwarePodDisruptionBudgetSchema()).Namespace(corev1.NamespaceDefault).Create(ctx, zpdb, metav1.CreateOptions{})
		require.ErrorContains(t, err, "ZoneAwarePodDisruptionBudget.rollout-operator.grafana.com \"ingester-zpdb\" is invalid", "Expected a ZoneAwarePodDisruptionBudget invalid configuration error")
	}

	{
		t.Log("Create a valid zpdb configuration.")
		awaitZoneAwarePodDisruptionBudgetCreation(t, ctx, cluster, path+yamlZpdbConfig)
	}

	{
		t.Log("Create 2 zones each with 2 pods.")
		createMockServiceZone(t, ctx, api, corev1.NamespaceDefault, "mock-zone-a", 2)
		createMockServiceZone(t, ctx, api, corev1.NamespaceDefault, "mock-zone-b", 2)
		requireEventuallyPod(t, api, ctx, "mock-zone-a-0", expectPodPhase(corev1.PodRunning), expectReady(), expectVersion("1"))
		requireEventuallyPod(t, api, ctx, "mock-zone-b-0", expectPodPhase(corev1.PodRunning), expectReady(), expectVersion("1"))
		requireEventuallyPod(t, api, ctx, "mock-zone-a-1", expectPodPhase(corev1.PodRunning), expectReady(), expectVersion("1"))
		requireEventuallyPod(t, api, ctx, "mock-zone-b-1", expectPodPhase(corev1.PodRunning), expectReady(), expectVersion("1"))
	}

	{
		t.Log("Cordon node")
		cordonNode(t, ctx, api)
	}

	{
		t.Log("Evict a pod.")
		ev := &policyv1beta1.Eviction{ObjectMeta: metav1.ObjectMeta{Name: "mock-zone-a-0", Namespace: corev1.NamespaceDefault}}
		require.NoError(t, api.PolicyV1beta1().Evictions(corev1.NamespaceDefault).Evict(ctx, ev))
	}

	{
		t.Log("Deny a pod eviction in the same zone.")
		ev := &policyv1beta1.Eviction{ObjectMeta: metav1.ObjectMeta{Name: "mock-zone-a-1", Namespace: corev1.NamespaceDefault}}
		require.ErrorContainsf(t, api.PolicyV1beta1().Evictions(corev1.NamespaceDefault).Evict(ctx, ev), "denied the request: 1 pod not ready in mock-zone-a", "Eviction denied")
	}

	{
		t.Log("Deny a pod eviction in another zone.")
		ev := &policyv1beta1.Eviction{ObjectMeta: metav1.ObjectMeta{Name: "mock-zone-b-0", Namespace: corev1.NamespaceDefault}}
		require.ErrorContainsf(t, api.PolicyV1beta1().Evictions(corev1.NamespaceDefault).Evict(ctx, ev), "denied the request: 1 pod not ready in mock-zone-a", "Eviction denied")
	}

	{
		t.Log("Un-cordon node")
		uncordonNode(t, ctx, api)
	}

	{
		t.Log("Await for evicted pod to be restarted.")
		awaitPodRunning(t, ctx, api, "mock-zone-a-0")
	}

	{
		t.Log("Evict a pod in a different zone.")
		ev := &policyv1beta1.Eviction{ObjectMeta: metav1.ObjectMeta{Name: "mock-zone-b-0", Namespace: corev1.NamespaceDefault}}
		require.NoError(t, api.PolicyV1beta1().Evictions(corev1.NamespaceDefault).Evict(ctx, ev))
	}
}

func TestZoneAwarePodDisruptionBudgetMaxUnavailableEq2(t *testing.T) {
	ctx := context.Background()

	cluster := k3t.NewCluster(ctx, t, k3t.WithImages("rollout-operator:latest", "mock-service:latest"))
	api := cluster.API()

	path := initManifestFiles(t, "max-unavailable-2")

	{
		t.Log("Create rollout operator and check it's running and ready.")
		createRolloutOperator(t, ctx, api, cluster.ExtAPI(), path, true)

		_ = createValidatingWebhookConfiguration(t, api, ctx, path+yamlWebhookZpdbValidation)
		_ = createValidatingWebhookConfiguration(t, api, ctx, path+yamlWebhookPodEviction)

		rolloutOperatorPod := eventuallyGetFirstPod(ctx, t, api, "name=rollout-operator")
		requireEventuallyPod(t, api, ctx, rolloutOperatorPod, expectPodPhase(corev1.PodRunning), expectReady())

		t.Log("Await CABundle assignment")
		require.Eventually(t, awaitCABundleAssignment(2, ctx, api), time.Second*30, time.Millisecond*10, "New webhooks have CABundle added")
	}

	{
		t.Log("Create a valid zpdb configuration.")
		awaitZoneAwarePodDisruptionBudgetCreation(t, ctx, cluster, path+yamlZpdbConfig)
	}

	{
		t.Log("Create 2 zones each with 2 pods.")
		createMockServiceZone(t, ctx, api, corev1.NamespaceDefault, "mock-zone-a", 2)
		createMockServiceZone(t, ctx, api, corev1.NamespaceDefault, "mock-zone-b", 2)
		requireEventuallyPod(t, api, ctx, "mock-zone-a-0", expectPodPhase(corev1.PodRunning), expectReady(), expectVersion("1"))
		requireEventuallyPod(t, api, ctx, "mock-zone-b-0", expectPodPhase(corev1.PodRunning), expectReady(), expectVersion("1"))
		requireEventuallyPod(t, api, ctx, "mock-zone-a-1", expectPodPhase(corev1.PodRunning), expectReady(), expectVersion("1"))
		requireEventuallyPod(t, api, ctx, "mock-zone-b-1", expectPodPhase(corev1.PodRunning), expectReady(), expectVersion("1"))
	}

	{
		t.Log("Cordon node")
		cordonNode(t, ctx, api)
	}

	{
		t.Log("Evict a pod.")
		ev := &policyv1beta1.Eviction{ObjectMeta: metav1.ObjectMeta{Name: "mock-zone-a-0", Namespace: corev1.NamespaceDefault}}
		require.NoError(t, api.PolicyV1beta1().Evictions(corev1.NamespaceDefault).Evict(ctx, ev))
	}

	{
		t.Log("Deny a pod eviction in another zone.")
		ev := &policyv1beta1.Eviction{ObjectMeta: metav1.ObjectMeta{Name: "mock-zone-b-0", Namespace: corev1.NamespaceDefault}}
		require.ErrorContainsf(t, api.PolicyV1beta1().Evictions(corev1.NamespaceDefault).Evict(ctx, ev), "denied the request: 1 pod not ready in mock-zone-a", "Eviction denied")
	}

	{
		t.Log("Allow a pod eviction in same zone.")
		ev := &policyv1beta1.Eviction{ObjectMeta: metav1.ObjectMeta{Name: "mock-zone-a-1", Namespace: corev1.NamespaceDefault}}
		require.NoError(t, api.PolicyV1beta1().Evictions(corev1.NamespaceDefault).Evict(ctx, ev))
	}
}

func TestZoneAwarePodDisruptionBudgetPartitionMode(t *testing.T) {
	ctx := context.Background()

	cluster := k3t.NewCluster(ctx, t, k3t.WithImages("rollout-operator:latest", "mock-service:latest"))
	api := cluster.API()

	path := initManifestFiles(t, "max-unavailable-1-with-regex")

	{
		t.Log("Create rollout operator and check it's running and ready.")
		createRolloutOperator(t, ctx, api, cluster.ExtAPI(), path, true)

		_ = createValidatingWebhookConfiguration(t, api, ctx, path+yamlWebhookZpdbValidation)
		_ = createValidatingWebhookConfiguration(t, api, ctx, path+yamlWebhookPodEviction)

		rolloutOperatorPod := eventuallyGetFirstPod(ctx, t, api, "name=rollout-operator")
		requireEventuallyPod(t, api, ctx, rolloutOperatorPod, expectPodPhase(corev1.PodRunning), expectReady())

		t.Log("Await CABundle assignment")
		require.Eventually(t, awaitCABundleAssignment(2, ctx, api), time.Second*30, time.Millisecond*10, "New webhooks have CABundle added")
	}

	{
		t.Log("Create a valid zpdb configuration.")
		awaitZoneAwarePodDisruptionBudgetCreation(t, ctx, cluster, path+yamlZpdbConfig)
	}

	{
		t.Log("Create 2 zones each with 2 pods. There are 2 partitions across 2 zones")
		createMockServiceZone(t, ctx, api, corev1.NamespaceDefault, "mock-zone-a", 2)
		createMockServiceZone(t, ctx, api, corev1.NamespaceDefault, "mock-zone-b", 2)
		requireEventuallyPod(t, api, ctx, "mock-zone-a-0", expectPodPhase(corev1.PodRunning), expectReady(), expectVersion("1"))
		requireEventuallyPod(t, api, ctx, "mock-zone-b-0", expectPodPhase(corev1.PodRunning), expectReady(), expectVersion("1"))
		requireEventuallyPod(t, api, ctx, "mock-zone-a-1", expectPodPhase(corev1.PodRunning), expectReady(), expectVersion("1"))
		requireEventuallyPod(t, api, ctx, "mock-zone-b-1", expectPodPhase(corev1.PodRunning), expectReady(), expectVersion("1"))
	}

	{
		t.Log("Cordon the node.")
		cordonNode(t, ctx, api)
	}

	{
		t.Log("Evict a pod in partition 0.")
		ev := &policyv1beta1.Eviction{ObjectMeta: metav1.ObjectMeta{Name: "mock-zone-a-0", Namespace: corev1.NamespaceDefault}}
		require.NoError(t, api.PolicyV1beta1().Evictions(corev1.NamespaceDefault).Evict(ctx, ev))
	}

	{
		t.Log("Deny a pod eviction in the same partition.")
		ev := &policyv1beta1.Eviction{ObjectMeta: metav1.ObjectMeta{Name: "mock-zone-b-0", Namespace: corev1.NamespaceDefault}}
		require.ErrorContainsf(t, api.PolicyV1beta1().Evictions(corev1.NamespaceDefault).Evict(ctx, ev), "denied the request: 1 pod not ready in partition 0", "Eviction denied")
	}

	{
		t.Log("Allow a pod eviction in another partition.")
		ev := &policyv1beta1.Eviction{ObjectMeta: metav1.ObjectMeta{Name: "mock-zone-b-1", Namespace: corev1.NamespaceDefault}}
		require.NoError(t, api.PolicyV1beta1().Evictions(corev1.NamespaceDefault).Evict(ctx, ev))
	}

	{
		t.Log("Deny a pod eviction in the same partition.")
		ev := &policyv1beta1.Eviction{ObjectMeta: metav1.ObjectMeta{Name: "mock-zone-a-1", Namespace: corev1.NamespaceDefault}}
		require.ErrorContainsf(t, api.PolicyV1beta1().Evictions(corev1.NamespaceDefault).Evict(ctx, ev), "denied the request: 1 pod not ready in partition 1", "Eviction denied")
	}

	{
		t.Log("Uncordon the node.")
		uncordonNode(t, ctx, api)
	}

	{
		t.Log("Await evicted node to restart")
		awaitPodRunning(t, ctx, api, "mock-zone-a-0")
	}

	{
		t.Log("Allow a different pod in same partition to now be deleted.")
		ev := &policyv1beta1.Eviction{ObjectMeta: metav1.ObjectMeta{Name: "mock-zone-b-0", Namespace: corev1.NamespaceDefault}}
		require.NoError(t, api.PolicyV1beta1().Evictions(corev1.NamespaceDefault).Evict(ctx, ev))
	}

}

func TestNoDownscale_CanDownscaleUnrelatedResource(t *testing.T) {
	ctx := context.Background()

	cluster := k3t.NewCluster(ctx, t, k3t.WithImages("rollout-operator:latest", "mock-service:latest"))
	api := cluster.API()

	path := initManifestFiles(t, "webhooks-enabled")

	{
		t.Log("Create the webhook before the rollout-operator, as rollout-operator should update its certificate.")
		_ = createValidatingWebhookConfiguration(t, api, ctx, path+yamlWebhookNoDownscale)
	}

	{
		t.Log("Create rollout operator and check it's running and ready.")
		createRolloutOperator(t, ctx, api, cluster.ExtAPI(), path, true)
		rolloutOperatorPod := eventuallyGetFirstPod(ctx, t, api, "name=rollout-operator")
		requireEventuallyPod(t, api, ctx, rolloutOperatorPod, expectPodPhase(corev1.PodRunning), expectReady())
	}

	mock := mockServiceStatefulSet("mock", "1", true, 1)
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

	path := initManifestFiles(t, "webhooks-enabled")

	{
		t.Log("Create the webhook before the rollout-operator, as rollout-operator should update its certificate.")
		_ = createValidatingWebhookConfiguration(t, api, ctx, path+yamlWebhookNoDownscale)
	}

	{
		t.Log("Create rollout operator and check it's running and ready.")
		createRolloutOperator(t, ctx, api, cluster.ExtAPI(), path, true)
		rolloutOperatorPod := eventuallyGetFirstPod(ctx, t, api, "name=rollout-operator")
		requireEventuallyPod(t, api, ctx, rolloutOperatorPod, expectPodPhase(corev1.PodRunning), expectReady())
	}

	mock := mockServiceStatefulSet("mock", "1", true, 1)
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

	path := initManifestFiles(t, "webhooks-enabled")

	{
		t.Log("Create the webhook before the rollout-operator, as rollout-operator should update its certificate.")
		_ = createValidatingWebhookConfiguration(t, api, ctx, path+yamlWebhookNoDownscale)
	}

	{
		t.Log("Create rollout operator and check it's running and ready.")
		createRolloutOperator(t, ctx, api, cluster.ExtAPI(), path, true)
		rolloutOperatorPod := eventuallyGetFirstPod(ctx, t, api, "name=rollout-operator")
		requireEventuallyPod(t, api, ctx, rolloutOperatorPod, expectPodPhase(corev1.PodRunning), expectReady())
	}

	mock := mockServiceStatefulSet("mock", "1", true, 1)
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

func cordonNode(t *testing.T, ctx context.Context, api *kubernetes.Clientset) {
	updateNodeCordon(t, ctx, api, true)
}

func uncordonNode(t *testing.T, ctx context.Context, api *kubernetes.Clientset) {
	updateNodeCordon(t, ctx, api, false)
}

func updateNodeCordon(t *testing.T, ctx context.Context, api *kubernetes.Clientset, cordon bool) {
	nodeList, err := api.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	require.Len(t, nodeList.Items, 1)
	node := nodeList.Items[0]

	node.Spec.Unschedulable = cordon
	_, err = api.CoreV1().Nodes().Update(ctx, &node, metav1.UpdateOptions{})
	require.NoError(t, err)
}

func awaitPodRunning(t *testing.T, ctx context.Context, api *kubernetes.Clientset, podname string) {
	awaitReadyRunning := func() bool {
		pod, err := api.CoreV1().Pods(corev1.NamespaceDefault).Get(ctx, podname, metav1.GetOptions{})
		if err != nil {
			return false
		}
		return util.IsPodRunningAndReady(pod)
	}
	require.Eventually(t, awaitReadyRunning, 30*time.Second, time.Millisecond*50)
}

func awaitZoneAwarePodDisruptionBudgetCreation(t *testing.T, ctx context.Context, cluster k3t.Cluster, configFile string) {
	task := func() bool {
		_, err := createZoneAwarePodDisruptionBudget(t, cluster, ctx, configFile)
		return err == nil
	}
	// note - this retry should not be needed, as the rollout-operator pod should be ready and running
	// however in the CI environments there have been intermittent errors which this retry is attempting to workaround
	require.Eventually(t, task, time.Second*30, time.Millisecond*10, "Zpdb configuration create failed")
}
