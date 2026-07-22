//go:build requires_docker

package integration

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"

	"github.com/grafana/rollout-operator/pkg/config"
)

func TestPhasedDeploymentHappyCase(t *testing.T) {
	ctx := context.Background()
	cluster := createKindCluster(t, "rollout-operator:latest", "mock-service:latest")
	api := cluster.API()

	path := initManifestFiles(t, "webhooks-enabled")
	createRolloutOperator(t, ctx, api, cluster.ExtAPI(), path, true)
	rolloutOperatorPod := eventuallyGetFirstPod(ctx, t, api, "name=rollout-operator")
	requireEventuallyPod(t, api, ctx, rolloutOperatorPod, expectPodPhase(corev1.PodRunning), expectReady())

	whPath := path + "admissionregistration.k8s.io-v1.MutatingWebhookConfiguration-phased-deployment-default.yaml"
	createMutatingWebhookConfiguration(t, api, ctx, whPath)
	require.Eventually(t, func() bool {
		list, err := api.AdmissionregistrationV1().MutatingWebhookConfigurations().List(ctx, metav1.ListOptions{
			LabelSelector: "grafana.com/inject-rollout-operator-ca=true,grafana.com/namespace=default",
		})
		if err != nil || len(list.Items) == 0 {
			return false
		}
		for _, wh := range list.Items {
			if strings.HasPrefix(wh.Name, "phased-deployment-") && len(wh.Webhooks) > 0 && len(wh.Webhooks[0].ClientConfig.CABundle) > 0 {
				return true
			}
		}
		return false
	}, 30*time.Second, 50*time.Millisecond, "phased webhook has CABundle")

	const rev1 = "r1"
	const rev2 = "r2"
	createPhasedMockDeployment(t, ctx, api, "frontend-zone-a", "", rev1, 1)
	createPhasedMockDeployment(t, ctx, api, "frontend-zone-b", "frontend-zone-a", rev1, 1)
	awaitDeploymentRolledOut(t, ctx, api, "frontend-zone-a")
	awaitDeploymentRolledOut(t, ctx, api, "frontend-zone-b")

	updatePhasedMockDeployment(t, ctx, api, "frontend-zone-a", "", rev2)
	updatePhasedMockDeployment(t, ctx, api, "frontend-zone-b", "frontend-zone-a", rev2)

	require.Eventually(t, func() bool {
		b, err := api.AppsV1().Deployments(corev1.NamespaceDefault).Get(ctx, "frontend-zone-b", metav1.GetOptions{})
		if err != nil {
			return false
		}
		return b.Spec.Paused && b.Annotations[config.RolloutDependencyPhaseAnnotationKey] != ""
	}, 30*time.Second, 200*time.Millisecond, "zone-b should be paused by phased gate")

	awaitDeploymentRolledOut(t, ctx, api, "frontend-zone-a")

	// Still paused during short soak (3s configured on the Deployment).
	time.Sleep(1 * time.Second)
	b, err := api.AppsV1().Deployments(corev1.NamespaceDefault).Get(ctx, "frontend-zone-b", metav1.GetOptions{})
	require.NoError(t, err)
	require.True(t, b.Spec.Paused, "zone-b should remain paused during soak")

	require.Eventually(t, func() bool {
		b, err := api.AppsV1().Deployments(corev1.NamespaceDefault).Get(ctx, "frontend-zone-b", metav1.GetOptions{})
		if err != nil {
			return false
		}
		return !b.Spec.Paused && b.Annotations[config.RolloutDependencyPhaseAnnotationKey] == config.RolloutDependencyPhaseComplete
	}, 30*time.Second, 200*time.Millisecond, "zone-b should unpause after soak")

	awaitDeploymentRolledOut(t, ctx, api, "frontend-zone-b")
}

func TestPhasedDeploymentResumeAfterBlock(t *testing.T) {
	ctx := context.Background()
	cluster := createKindCluster(t, "rollout-operator:latest", "mock-service:latest")
	api := cluster.API()

	path := initManifestFiles(t, "webhooks-not-enabled")
	createRolloutOperator(t, ctx, api, cluster.ExtAPI(), path, false)
	rolloutOperatorPod := eventuallyGetFirstPod(ctx, t, api, "name=rollout-operator")
	requireEventuallyPod(t, api, ctx, rolloutOperatorPod, expectPodPhase(corev1.PodRunning), expectReady())

	const rev = "r-block"
	createPhasedMockDeployment(t, ctx, api, "frontend-zone-a", "", rev, 1)
	awaitDeploymentRolledOut(t, ctx, api, "frontend-zone-a")

	replicas := int32(1)
	b := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name: "frontend-zone-b",
			Labels: map[string]string{
				config.RolloutPhasedLabelKey: config.RolloutPhasedLabelValue,
				"name":                       "frontend-zone-b",
			},
			Annotations: map[string]string{
				config.RolloutDependsOnAnnotationKey:          "frontend-zone-a",
				config.RolloutRevisionAnnotationKey:           rev,
				config.RolloutSoakDurationAnnotationKey:       "3s",
				config.RolloutRestartThresholdAnnotationKey:   "10%",
				config.RolloutDependencyPhaseAnnotationKey:    config.RolloutDependencyPhaseBlocked,
				config.RolloutDependencyRevisionAnnotationKey: rev,
				config.RolloutDependencyReasonAnnotationKey:   "test block",
				config.RolloutHadPausedAnnotationKey:          "false",
			},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Paused:   true,
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"name": "frontend-zone-b"}},
			Template: phasedMockPodTemplate("frontend-zone-b", "1"),
		},
	}
	_, err := api.AppsV1().Deployments(corev1.NamespaceDefault).Create(ctx, b, metav1.CreateOptions{})
	require.NoError(t, err)

	_, err = api.AppsV1().Deployments(corev1.NamespaceDefault).Patch(ctx, "frontend-zone-b",
		types.MergePatchType,
		[]byte(`{"metadata":{"annotations":{"grafana.com/rollout-resume":"r-block"}}}`),
		metav1.PatchOptions{})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		cur, err := api.AppsV1().Deployments(corev1.NamespaceDefault).Get(ctx, "frontend-zone-b", metav1.GetOptions{})
		if err != nil {
			return false
		}
		return !cur.Spec.Paused && cur.Annotations[config.RolloutDependencyPhaseAnnotationKey] == config.RolloutDependencyPhaseComplete
	}, 30*time.Second, 200*time.Millisecond, "resume should complete the gate")
}

func createPhasedMockDeployment(t *testing.T, ctx context.Context, api *kubernetes.Clientset, name, dependsOn, revision string, replicas int32) {
	t.Helper()
	labels := map[string]string{
		config.RolloutPhasedLabelKey: config.RolloutPhasedLabelValue,
		"name":                       name,
	}
	ann := map[string]string{
		config.RolloutRevisionAnnotationKey:     revision,
		config.RolloutSoakDurationAnnotationKey: "3s",
	}
	if dependsOn != "" {
		ann[config.RolloutDependsOnAnnotationKey] = dependsOn
		// Treat the initial revision as already gated so CREATE does not pause the first rollout.
		ann[config.RolloutDependencyPhaseAnnotationKey] = config.RolloutDependencyPhaseComplete
		ann[config.RolloutDependencyRevisionAnnotationKey] = revision
	}
	d := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Labels:      labels,
			Annotations: ann,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"name": name}},
			Strategy: appsv1.DeploymentStrategy{
				Type: appsv1.RollingUpdateDeploymentStrategyType,
				RollingUpdate: &appsv1.RollingUpdateDeployment{
					MaxUnavailable: intstrPtr(0),
					MaxSurge:       intstrPtr(1),
				},
			},
			Template: phasedMockPodTemplate(name, "1"),
		},
	}
	_, err := api.AppsV1().Deployments(corev1.NamespaceDefault).Create(ctx, d, metav1.CreateOptions{})
	require.NoError(t, err)
}

func updatePhasedMockDeployment(t *testing.T, ctx context.Context, api *kubernetes.Clientset, name, dependsOn, revision string) {
	t.Helper()
	d, err := api.AppsV1().Deployments(corev1.NamespaceDefault).Get(ctx, name, metav1.GetOptions{})
	require.NoError(t, err)
	if d.Annotations == nil {
		d.Annotations = map[string]string{}
	}
	d.Annotations[config.RolloutRevisionAnnotationKey] = revision
	if dependsOn != "" {
		d.Annotations[config.RolloutDependsOnAnnotationKey] = dependsOn
	}
	d.Spec.Template = phasedMockPodTemplate(name, "2")
	_, err = api.AppsV1().Deployments(corev1.NamespaceDefault).Update(ctx, d, metav1.UpdateOptions{})
	require.NoError(t, err)
}

func phasedMockPodTemplate(name, version string) corev1.PodTemplateSpec {
	return corev1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{
			Labels: map[string]string{"name": name, "version": version},
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{
				Name:            "app",
				Image:           "mock-service:latest",
				ImagePullPolicy: corev1.PullNever,
				Ports:           []corev1.ContainerPort{{ContainerPort: 8080}},
				Env: []corev1.EnvVar{
					{Name: "VERSION", Value: version},
					{Name: "READY", Value: "true"},
				},
				ReadinessProbe: &corev1.Probe{
					ProbeHandler: corev1.ProbeHandler{
						HTTPGet: &corev1.HTTPGetAction{Path: "/ready", Port: intstr.FromInt(8080)},
					},
					InitialDelaySeconds: 1,
					PeriodSeconds:       1,
				},
			}},
		},
	}
}

func awaitDeploymentRolledOut(t *testing.T, ctx context.Context, api *kubernetes.Clientset, name string) {
	t.Helper()
	require.Eventually(t, func() bool {
		d, err := api.AppsV1().Deployments(corev1.NamespaceDefault).Get(ctx, name, metav1.GetOptions{})
		if err != nil || d.Spec.Paused {
			return false
		}
		desired := int32(1)
		if d.Spec.Replicas != nil {
			desired = *d.Spec.Replicas
		}
		return d.Status.ObservedGeneration >= d.Generation &&
			d.Status.UpdatedReplicas == desired &&
			d.Status.ReadyReplicas == desired &&
			d.Status.AvailableReplicas == desired
	}, 2*time.Minute, 500*time.Millisecond, "deployment %s should be fully rolled out", name)
}

func intstrPtr(v int) *intstr.IntOrString {
	i := intstr.FromInt(v)
	return &i
}

func createMutatingWebhookConfiguration(t *testing.T, api *kubernetes.Clientset, ctx context.Context, file string) *admissionregistrationv1.MutatingWebhookConfiguration {
	t.Helper()
	wh := loadFromDisk[admissionregistrationv1.MutatingWebhookConfiguration](t, file, &admissionregistrationv1.MutatingWebhookConfiguration{})
	created, err := api.AdmissionregistrationV1().MutatingWebhookConfigurations().Create(ctx, wh, metav1.CreateOptions{})
	require.NoError(t, err)
	return created
}
