//go:build requires_docker

package integration

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apiextensionsclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/kubernetes"
)

const certificateSecretName = "certificate"

func createRolloutOperator(t *testing.T, ctx context.Context, api *kubernetes.Clientset, extApi *apiextensionsclient.Clientset, namespace string, webhook bool) {
	createRolloutOperatorDependencies(t, ctx, api, extApi, namespace, webhook)

	_, err := api.AppsV1().Deployments(namespace).Create(ctx, rolloutOperatorDeployment(namespace, webhook), metav1.CreateOptions{})
	require.NoError(t, err)
}

func createRolloutOperatorDependencies(t *testing.T, ctx context.Context, api *kubernetes.Clientset, extApi *apiextensionsclient.Clientset, namespace string, webhook bool) {
	_, err := api.CoreV1().ServiceAccounts(namespace).Create(ctx, rolloutOperatorServiceAccount(), metav1.CreateOptions{})
	require.NoError(t, err)

	_, err = api.RbacV1().Roles(namespace).Create(ctx, rolloutOperatorRole(), metav1.CreateOptions{})
	require.NoError(t, err)

	_, err = api.RbacV1().RoleBindings(namespace).Create(ctx, rolloutOperatorRoleBinding(namespace), metav1.CreateOptions{})
	require.NoError(t, err)

	crd, err := zoneAwarePodDistruptionBudgetCustomResourceDefinition()
	require.NoError(t, err)

	_, err = extApi.ApiextensionsV1().CustomResourceDefinitions().
		Create(context.Background(), crd, metav1.CreateOptions{})
	require.NoError(t, err)

	if webhook {
		_, err = api.RbacV1().Roles(namespace).Create(ctx, webhookRolloutOperatorRole(), metav1.CreateOptions{})
		require.NoError(t, err)

		_, err = api.RbacV1().RoleBindings(namespace).Create(ctx, webhookRolloutOperatorRoleBinding(namespace), metav1.CreateOptions{})
		require.NoError(t, err)

		_, err := api.RbacV1().ClusterRoles().Create(ctx, webhookRolloutOperatorClusterRole(namespace), metav1.CreateOptions{})
		require.NoError(t, err)

		_, err = api.RbacV1().ClusterRoleBindings().Create(ctx, webhookRolloutOperatorClusterRoleBinding(namespace), metav1.CreateOptions{})
		require.NoError(t, err)

		_, err = api.CoreV1().Services(namespace).Create(ctx, rolloutOperatorService(), metav1.CreateOptions{})
		require.NoError(t, err)
	}
}

func rolloutOperatorDeployment(namespace string, webhook bool) *appsv1.Deployment {
	args := []string{
		fmt.Sprintf("-kubernetes.namespace=%s", namespace),
		"-reconcile.interval=1s",
		"-log.level=debug",
	}
	if webhook {
		args = append(args,
			"-server-tls.enabled=true",
			"-server-tls.self-signed-cert.secret-name="+certificateSecretName,
		)
	}

	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name: "rollout-operator",
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr[int32](1),
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"name": "rollout-operator",
				},
			},
			Strategy: appsv1.DeploymentStrategy{
				Type: appsv1.RollingUpdateDeploymentStrategyType,
				RollingUpdate: &appsv1.RollingUpdateDeployment{
					MaxUnavailable: &intstr.IntOrString{
						Type:   intstr.Int,
						IntVal: 1,
					},
					MaxSurge: &intstr.IntOrString{
						Type:   intstr.Int,
						IntVal: 0,
					},
				},
			},
			MinReadySeconds: 10,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"name": "rollout-operator",
					},
				},
				Spec: corev1.PodSpec{
					ServiceAccountName: "rollout-operator",
					Containers: []corev1.Container{
						{
							Name:            "rollout-operator",
							Image:           "rollout-operator:latest",
							Args:            args,
							ImagePullPolicy: "IfNotPresent",
							Ports: []corev1.ContainerPort{
								{
									Name:          "http-metrics",
									ContainerPort: 8001,
								},
							},
							ReadinessProbe: &corev1.Probe{
								ProbeHandler: corev1.ProbeHandler{
									HTTPGet: &corev1.HTTPGetAction{
										Path: "/ready",
										Port: intstr.FromInt(8001),
									},
								},
								InitialDelaySeconds: 1,
								TimeoutSeconds:      1,
							},
						},
					},
				},
			},
		},
	}
}

func rolloutOperatorServiceAccount() *corev1.ServiceAccount {
	return &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name: "rollout-operator",
		},
	}
}

func rolloutOperatorRoleBinding(namespace string) *rbacv1.RoleBinding {
	return &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: "rollout-operator-rolebinding",
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "Role",
			Name:     "rollout-operator-role",
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:      "ServiceAccount",
				Name:      "rollout-operator",
				Namespace: namespace,
			},
		},
	}

}

// rolloutOperatorRole provides the role for the "default" rollout-operator functionality.
func rolloutOperatorRole() *rbacv1.Role {
	return &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{
			Name: "rollout-operator-role",
		},
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups: []string{""},
				Resources: []string{"pods"},
				Verbs:     []string{"list", "get", "watch", "delete"},
			},
			{
				APIGroups: []string{"apps"},
				Resources: []string{"statefulsets"},
				Verbs:     []string{"list", "get", "watch"},
			},
			{
				APIGroups: []string{"apps"},
				Resources: []string{"statefulsets/status"},
				Verbs:     []string{"update"},
			},
			{
				APIGroups: []string{"rollout-operator.grafana.com"},
				Resources: []string{"zoneawarepoddisruptionbudgets"},
				Verbs:     []string{"get", "list", "watch"},
			},
		},
	}
}
func rolloutOperatorService() *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name: "rollout-operator",
		},
		Spec: corev1.ServiceSpec{
			Type: corev1.ServiceTypeClusterIP,
			Selector: map[string]string{
				"name": "rollout-operator",
			},
			Ports: []corev1.ServicePort{
				{
					Name:       "https",
					Protocol:   corev1.ProtocolTCP,
					Port:       443,
					TargetPort: intstr.FromInt(8443),
				},
			},
			PublishNotReadyAddresses: true, // We want to control them even if they're not ready.
		},
	}
}

// webhookRolloutOperatorRole provides the role for the rollout-operator with required permissions to create secrets
// that store the webhook certificate and to edit the validation webhooks
func webhookRolloutOperatorRole() *rbacv1.Role {
	return &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{
			Name: "rollout-operator-webhook-role",
		},
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups:     []string{""},
				Resources:     []string{"secrets"},
				ResourceNames: []string{certificateSecretName},
				Verbs:         []string{"update", "get"},
			},
			{
				APIGroups: []string{""},
				Resources: []string{"secrets"},
				Verbs:     []string{"create"},
			},
		},
	}
}

func webhookRolloutOperatorRoleBinding(namespace string) *rbacv1.RoleBinding {
	return &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: "rollout-operator-webhook-rolebinding",
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "Role",
			Name:     "rollout-operator-webhook-role",
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:      "ServiceAccount",
				Name:      "rollout-operator",
				Namespace: namespace,
			},
		},
	}
}

func webhookRolloutOperatorClusterRoleBinding(namespace string) *rbacv1.ClusterRoleBinding {
	return &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: fmt.Sprintf("rollout-operator-webhook-%s-clusterrolebinding", namespace),
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "ClusterRole",
			Name:     fmt.Sprintf("rollout-operator-webhook-%s-clusterrole", namespace),
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:      "ServiceAccount",
				Name:      "rollout-operator",
				Namespace: namespace,
			},
		},
	}
}

func webhookRolloutOperatorClusterRole(namespace string) *rbacv1.ClusterRole {
	return &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{
			Name: fmt.Sprintf("rollout-operator-webhook-%s-clusterrole", namespace),
		},
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups: []string{"admissionregistration.k8s.io"},
				Resources: []string{"validatingwebhookconfigurations", "mutatingwebhookconfigurations"},
				Verbs:     []string{"list", "patch", "watch"},
			},
		},
	}
}

var (
	scheme       = runtime.NewScheme()
	codecs       = serializer.NewCodecFactory(scheme)
	deserializer = codecs.UniversalDeserializer()
	// this is the namespace used in the ../development examples
	templateNamespace = "rollout-operator-development"
)

func loadConfigurationFromDisk(path, namespaceOld, namespaceNew string, into runtime.Object) (*runtime.Object, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read file from disk: %w", err)
	}

	var str = string(data)
	if len(namespaceOld) > 0 || len(namespaceNew) > 0 {
		str = strings.ReplaceAll(string(data), namespaceOld, namespaceNew)
	}

	jsonData, err := yaml.ToJSON([]byte(str))
	if err != nil {
		return nil, fmt.Errorf("failed to convert yaml to json: %w", err)
	}

	obj, _, err := deserializer.Decode(jsonData, nil, into)
	if err != nil {
		return nil, fmt.Errorf("failed to decode from json: %w", err)
	}

	return &obj, nil
}

func loadCustomResourceDefinitionFromDisk(path string) (*apiextensionsv1.CustomResourceDefinition, error) {
	object, err := loadConfigurationFromDisk(path, "", "", &apiextensionsv1.CustomResourceDefinition{})
	if err != nil {
		return nil, err
	}
	obj := *object
	crd, ok := obj.(*apiextensionsv1.CustomResourceDefinition)
	if !ok {
		return nil, fmt.Errorf("decoded object is not a CustomResourceDefinition")
	}
	return crd, nil
}

func loadValidatingWebhookConfigurationFromDisk(path, namespaceOld, namespaceNew string) (*admissionregistrationv1.ValidatingWebhookConfiguration, error) {
	object, err := loadConfigurationFromDisk(path, namespaceOld, namespaceNew, &admissionregistrationv1.ValidatingWebhookConfiguration{})
	if err != nil {
		return nil, err
	}
	obj := *object
	webhook, ok := obj.(*admissionregistrationv1.ValidatingWebhookConfiguration)
	if !ok {
		return nil, fmt.Errorf("decoded object is not a ValidatingWebhookConfiguration")
	}
	return webhook, nil
}

func zoneAwarePodDistruptionBudgetCustomResourceDefinition() (*apiextensionsv1.CustomResourceDefinition, error) {
	return loadCustomResourceDefinitionFromDisk("../development/zone-aware-pod-disruption-budget-custom-resource-definition.yaml")
}

func zpdbValidatingWebhook(namespace string) (*admissionregistrationv1.ValidatingWebhookConfiguration, error) {
	return loadValidatingWebhookConfigurationFromDisk("../development/zone-aware-pod-disruption-budget-validating-webhook.yaml", templateNamespace, namespace)
}

func podEvictionValidatingWebhook(namespace string) (*admissionregistrationv1.ValidatingWebhookConfiguration, error) {
	return loadValidatingWebhookConfigurationFromDisk("../development/eviction-webhook.yaml", templateNamespace, namespace)
}

func noDownscaleValidatingWebhook(namespace string) *admissionregistrationv1.ValidatingWebhookConfiguration {
	return &admissionregistrationv1.ValidatingWebhookConfiguration{
		ObjectMeta: metav1.ObjectMeta{
			Name: fmt.Sprintf("no-downscale-%s", namespace),
			Labels: map[string]string{
				"grafana.com/inject-rollout-operator-ca": "true",
				"grafana.com/namespace":                  namespace,
			},
			Annotations:     nil,
			OwnerReferences: nil,
			Finalizers:      nil,
			ManagedFields:   nil,
		},
		Webhooks: []admissionregistrationv1.ValidatingWebhook{
			{
				Name: fmt.Sprintf("no-downscale-%s.grafana.com", namespace),
				ClientConfig: admissionregistrationv1.WebhookClientConfig{
					Service: &admissionregistrationv1.ServiceReference{
						Namespace: namespace,
						Name:      "rollout-operator",
						Path:      ptr("/admission/no-downscale"),
					},
				},
				Rules: []admissionregistrationv1.RuleWithOperations{
					{
						Operations: []admissionregistrationv1.OperationType{admissionregistrationv1.Update},
						Rule: admissionregistrationv1.Rule{
							APIGroups:   []string{"apps"},
							APIVersions: []string{"v1"},
							Resources: []string{
								"statefulsets",
								"deployments",
								"replicasets",
								"statefulsets/scale",
								"deployments/scale",
								"replicasets/scale",
							},
							Scope: ptr(admissionregistrationv1.NamespacedScope),
						},
					},
				},
				NamespaceSelector: &metav1.LabelSelector{
					// This is just an example of matching changes only in a specific namespace.
					// https://kubernetes.io/docs/reference/labels-annotations-taints/#kubernetes-io-metadata-name
					MatchLabels: map[string]string{"kubernetes.io/metadata.name": namespace},
				},
				FailurePolicy:           ptr(admissionregistrationv1.Fail),
				SideEffects:             ptr(admissionregistrationv1.SideEffectClassNone),
				AdmissionReviewVersions: []string{"v1"},
			},
		},
	}
}
