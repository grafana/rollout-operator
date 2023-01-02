//go:build requires_docker

package integration

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"
)

func createRolloutOperator(t *testing.T, ctx context.Context, api *kubernetes.Clientset) {
	_, err := api.CoreV1().ServiceAccounts(corev1.NamespaceDefault).Create(ctx, rolloutOperatorServiceAccount(), metav1.CreateOptions{})
	require.NoError(t, err)

	_, err = api.RbacV1().Roles(corev1.NamespaceDefault).Create(ctx, rolloutOperatorRBAC(), metav1.CreateOptions{})
	require.NoError(t, err)

	_, err = api.RbacV1().RoleBindings(corev1.NamespaceDefault).Create(ctx, rolloutOperatorRoleBinding(), metav1.CreateOptions{})
	require.NoError(t, err)

	_, err = api.AppsV1().Deployments(corev1.NamespaceDefault).Create(ctx, rolloutOperatorDeployment(corev1.NamespaceDefault), metav1.CreateOptions{})
	require.NoError(t, err)
}

func rolloutOperatorDeployment(namespace string) *appsv1.Deployment {
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
							Name:  "rollout-operator",
							Image: "rollout-operator:latest",
							Args: []string{
								fmt.Sprintf("-kubernetes.namespace=%s", namespace),
								"-reconcile.interval=1s",
							},
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
			Name:      "rollout-operator",
			Namespace: "default",
		},
	}
}

func rolloutOperatorRoleBinding() *rbacv1.RoleBinding {
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
				Namespace: "default",
			},
		},
	}

}

func rolloutOperatorRBAC() *rbacv1.Role {
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
