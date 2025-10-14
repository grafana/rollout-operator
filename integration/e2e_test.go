//go:build requires_docker

package integration

import (
	"bufio"
	"context"
	"encoding/json"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
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

		go func() {
			podLogOpts := corev1.PodLogOptions{
				Follow: true, // set true to stream logs continuously
			}

			req := api.CoreV1().Pods(corev1.NamespaceDefault).GetLogs(rolloutOperatorPod, &podLogOpts)
			reader, err := req.Stream(ctx)
			require.NoError(t, err)
			defer reader.Close()

			scanner := bufio.NewScanner(reader)
			for scanner.Scan() {
				t.Logf("[rollout-operator] - %s\n", scanner.Text())
			}
		}()
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
		evictPodWithDebug(t, ctx, api, "mock-zone-a-1", "mock-zone-a", "denied the request: 1 pod not ready in mock-zone-a", []string{"mock-zone-a-0"})
	}

	{
		t.Log("Deny a pod eviction in another zone.")
		evictPodWithDebug(t, ctx, api, "mock-zone-b-0", "mock-zone-b", "denied the request: 1 pod not ready in mock-zone-a", []string{"mock-zone-a-0"})
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

		go func() {
			podLogOpts := corev1.PodLogOptions{
				Follow: true, // set true to stream logs continuously
			}

			req := api.CoreV1().Pods(corev1.NamespaceDefault).GetLogs(rolloutOperatorPod, &podLogOpts)
			reader, err := req.Stream(ctx)
			require.NoError(t, err)
			defer reader.Close()

			scanner := bufio.NewScanner(reader)
			for scanner.Scan() {
				t.Logf("[rollout-operator] - %s\n", scanner.Text())
			}
		}()
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
		evictPodWithDebug(t, ctx, api, "mock-zone-b-0", "mock-zone-b", "denied the request: 1 pod not ready in mock-zone-a", []string{"mock-zone-a-0"})
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

		go func() {
			podLogOpts := corev1.PodLogOptions{
				Follow: true, // set true to stream logs continuously
			}

			req := api.CoreV1().Pods(corev1.NamespaceDefault).GetLogs(rolloutOperatorPod, &podLogOpts)
			reader, err := req.Stream(ctx)
			require.NoError(t, err)
			defer reader.Close()

			scanner := bufio.NewScanner(reader)
			for scanner.Scan() {
				t.Logf("[rollout-operator] - %s\n", scanner.Text())
			}
		}()
	}

	{
		t.Log("Create a valid zpdb configuration.")
		awaitZoneAwarePodDisruptionBudgetCreation(t, ctx, cluster, path+yamlZpdbConfig)
	}

	{
		t.Log("Create 2 zones each with 2 pods. There are 2 partitions across 2 zones")
		createMockServiceZone(t, ctx, api, corev1.NamespaceDefault, "mock-zone-a", 2)
		createMockServiceZone(t, ctx, api, corev1.NamespaceDefault, "mock-zone-b", 2)
		requireEventuallyPod(t, api, ctx, "mock-zone-a-0", expectPodPhase(corev1.PodRunning), expectedPodRunningAndReady(), expectVersion("1"))
		requireEventuallyPod(t, api, ctx, "mock-zone-b-0", expectPodPhase(corev1.PodRunning), expectedPodRunningAndReady(), expectVersion("1"))
		requireEventuallyPod(t, api, ctx, "mock-zone-a-1", expectPodPhase(corev1.PodRunning), expectedPodRunningAndReady(), expectVersion("1"))
		requireEventuallyPod(t, api, ctx, "mock-zone-b-1", expectPodPhase(corev1.PodRunning), expectedPodRunningAndReady(), expectVersion("1"))
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
		evictPodEventually(t, ctx, api, "mock-zone-b-1", []string{"mock-zone-a-0", "mock-zone-b-0", "mock-zone-a-1"})
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
		evictPodEventually(t, ctx, api, "mock-zone-b-0", []string{"mock-zone-a-0", "mock-zone-a-1", "mock-zone-b-1"})
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
		return err != nil
	}
	// note - this retry should not be needed, as the rollout-operator pod should be ready and running
	// however in the CI environments there have been intermittent errors which this retry is attempting to workaround
	require.Eventually(t, task, time.Second*30, time.Millisecond*10, "Zpdb configuration create failed")
}

func evictPodEventually(t *testing.T, ctx context.Context, api *kubernetes.Clientset, podToEvict string, relatedPods []string) {
	task := func() bool {
		t.Log("Attempting to evicting pod ", podToEvict)
		ev := &policyv1beta1.Eviction{ObjectMeta: metav1.ObjectMeta{Name: podToEvict, Namespace: corev1.NamespaceDefault}}
		err := api.PolicyV1beta1().Evictions(corev1.NamespaceDefault).Evict(ctx, ev)

		if err != nil {
			t.Logf("Eviction failed for %s. %v", podToEvict, err)
			for _, relatedPod := range relatedPods {
				pod, err := api.CoreV1().Pods(corev1.NamespaceDefault).Get(ctx, relatedPod, metav1.GetOptions{})
				if err != nil {
					t.Logf("Pod %s. error=%v", pod.Name, err)
				} else {
					t.Logf("Pod %s. phase=%s, readyRunning=%v", pod.Name, pod.Status.Phase, util.IsPodRunningAndReady(pod))
				}
			}
		}

		return err == nil
	}
	require.Eventually(t, task, time.Second*10, time.Millisecond*50, "Unable to evict pod %s", podToEvict)
}

func evictPodWithDebug(t *testing.T, ctx context.Context, api *kubernetes.Clientset, podToEvict string, podSts string, expectedError string, relatedPods []string) {

	pod, err := api.CoreV1().Pods(corev1.NamespaceDefault).Get(ctx, podToEvict, metav1.GetOptions{})
	require.NoError(t, err)

	sts, err := api.AppsV1().StatefulSets(corev1.NamespaceDefault).Get(ctx, podSts, metav1.GetOptions{})
	require.NoError(t, err)

	tested, notReady, unknown, err := podsNotRunningAndReady(t, ctx, api, sts, pod)
	t.Logf("Pre-eviction reporting on related pods. pod=%s, sts=%s, tested=%d, notReady=%d, unknown=%d", pod.Name, sts.Name, tested, notReady, unknown)

	for _, relatedPod := range relatedPods {
		relatedPod, err := api.CoreV1().Pods(corev1.NamespaceDefault).Get(ctx, relatedPod, metav1.GetOptions{})
		require.NoError(t, err)
		t.Logf("Pod %s. phase=%s, readyRunning=%v", relatedPod.Name, relatedPod.Status.Phase, util.IsPodRunningAndReady(relatedPod))
	}

	t.Logf("Evicting pod. pod=%s", pod.Name)
	ev := &policyv1beta1.Eviction{ObjectMeta: metav1.ObjectMeta{Name: pod.Name, Namespace: corev1.NamespaceDefault}}
	err = api.PolicyV1beta1().Evictions(corev1.NamespaceDefault).Evict(ctx, ev)
	if err != nil {
		// happy path
		require.ErrorContainsf(t, err, expectedError, "Eviction denied")
		return
	}
	// we should not be here!
	t.Logf("We should not be here! Eviction was allowed for %s", pod.Name)

	tested, notReady, unknown, err = podsNotRunningAndReady(t, ctx, api, sts, pod)
	t.Logf("Pst-eviction reporting on related pods. pod=%s, sts=%s, tested=%d, notReady=%d, unknown=%d", pod.Name, sts.Name, tested, notReady, unknown)

	for _, relatedPod := range relatedPods {
		relatedPod, err := api.CoreV1().Pods(corev1.NamespaceDefault).Get(ctx, relatedPod, metav1.GetOptions{})
		require.NoError(t, err)
		t.Logf("Pod %s. phase=%s, readyRunning=%v", relatedPod.Name, relatedPod.Status.Phase, util.IsPodRunningAndReady(relatedPod))
	}
	require.Fail(t, "Eviction should have been rejected")

}

// tested: 0, notReady: 0, unknown
func podsNotRunningAndReady(t *testing.T, ctx context.Context, api *kubernetes.Clientset, sts *appsv1.StatefulSet, pod *corev1.Pod) (int, int, int, error) {
	podsSelector := labels.NewSelector().Add(
		util.MustNewLabelsRequirement("name", selection.Equals, []string{sts.Spec.Template.Labels["name"]}),
	)

	list, err := api.CoreV1().Pods(sts.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: podsSelector.String(),
	})

	if err != nil {
		return 0, 0, 0, err
	}

	var replicas int
	if sts.Spec.Replicas == nil {
		replicas = int(sts.Status.Replicas)
	} else {
		replicas = max(int(sts.Status.Replicas), int(*sts.Spec.Replicas))
	}

	notReady := 0
	tested := 0
	unknown := 0

	for _, pd := range list.Items {

		if pod.UID != pd.UID && !util.IsPodRunningAndReady(&pd) {
			notReady++
		}

		tested++
	}

	// we consider the pod as not ready if there should be a given replica count but it is not yet being found in the pods query
	// note that the effect here is that we do not know which partition these other pods will be in, so we have to attribute them to this partition to be safe
	if len(list.Items) < replicas {
		unknown = replicas - len(list.Items)
	}

	return tested, notReady, unknown, nil
}
