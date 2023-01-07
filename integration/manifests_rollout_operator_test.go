//go:build requires_docker

package integration

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"
)

func createRolloutOperator(t *testing.T, ctx context.Context, api *kubernetes.Clientset, namespace string, webhook bool) {
	_, err := api.CoreV1().ServiceAccounts(namespace).Create(ctx, rolloutOperatorServiceAccount(), metav1.CreateOptions{})
	require.NoError(t, err)

	_, err = api.RbacV1().Roles(namespace).Create(ctx, rolloutOperatorRole(), metav1.CreateOptions{})
	require.NoError(t, err)

	_, err = api.RbacV1().RoleBindings(namespace).Create(ctx, rolloutOperatorRoleBinding(namespace), metav1.CreateOptions{})
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

	_, err = api.AppsV1().Deployments(namespace).Create(ctx, rolloutOperatorDeployment(namespace, webhook), metav1.CreateOptions{})
	require.NoError(t, err)
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
				APIGroups: []string{""},
				Resources: []string{"secrets"},
				Verbs:     []string{"create", "update", "get"},
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
			Name: fmt.Sprintf("rollout-operator-wenhook-%s-clusterrolebinding", namespace),
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "ClusterRole",
			Name:     fmt.Sprintf("rollout-operator-wenhook-%s-clusterrole", namespace),
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
			Name: fmt.Sprintf("rollout-operator-wenhook-%s-clusterrole", namespace),
		},
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups: []string{"admissionregistration.k8s.io"},
				Resources: []string{"validatingwebhookconfigurations"},
				Verbs:     []string{"list", "patch"},
			},
		},
	}
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
								"deploymentsets",
								"replicasets",
								"statefulsets/scale",
								"deploymentsets/scale",
								"replicasets/scale",
							},
							Scope: ptr(admissionregistrationv1.NamespacedScope),
						},
					},
				},
				FailurePolicy:           ptr(admissionregistrationv1.Fail),
				SideEffects:             ptr(admissionregistrationv1.SideEffectClassNone),
				AdmissionReviewVersions: []string{"v1"},
			},
		},
	}
}
