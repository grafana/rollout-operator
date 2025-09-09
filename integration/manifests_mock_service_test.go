//go:build requires_docker

package integration

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"

	rcfg "github.com/grafana/rollout-operator/pkg/config"
	zpdb "github.com/grafana/rollout-operator/pkg/zpdb"
)

func createMockServiceZone(t *testing.T, ctx context.Context, api *kubernetes.Clientset, namespace, name string, replicas int) {
	t.Helper()
	{
		_, err := api.AppsV1().StatefulSets(namespace).Create(ctx, mockServiceStatefulSet(name, "1", true, replicas), metav1.CreateOptions{})
		require.NoError(t, err, "Can't create StatefulSet")
	}

	{
		_, err := api.CoreV1().Services(namespace).Create(ctx, mockServiceService(name), metav1.CreateOptions{})
		require.NoError(t, err, "Can't create Service")
	}
	{
		_, err := api.NetworkingV1().Ingresses(namespace).Create(ctx, mockServiceIngress(name), metav1.CreateOptions{})
		require.NoError(t, err, "Can't create Ingress")
	}
}

func mockServiceService(name string) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: corev1.ServiceSpec{
			Type: corev1.ServiceTypeClusterIP,
			Selector: map[string]string{
				"name": name,
			},
			Ports: []corev1.ServicePort{
				{
					Name:       "http",
					Protocol:   corev1.ProtocolTCP,
					Port:       8080,
					TargetPort: intstr.FromInt(8080),
				},
			},
			PublishNotReadyAddresses: true, // We want to control them even if they're not ready.
		},
	}
}

func mockServiceIngress(name string) *networkingv1.Ingress {
	path := networkingv1.HTTPIngressPath{
		Path:     pathPrefix(name),
		PathType: ptr(networkingv1.PathTypePrefix),
		Backend: networkingv1.IngressBackend{
			Service: &networkingv1.IngressServiceBackend{
				Name: name,
				Port: networkingv1.ServiceBackendPort{
					Number: 8080,
				},
			},
		},
	}
	rule := networkingv1.IngressRule{
		IngressRuleValue: networkingv1.IngressRuleValue{
			HTTP: &networkingv1.HTTPIngressRuleValue{
				Paths: []networkingv1.HTTPIngressPath{path},
			},
		},
	}

	return &networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			Annotations: map[string]string{
				"ingress.kubernetes.io/ssl-redirect": "false",
			},
		},
		Spec: networkingv1.IngressSpec{
			Rules: []networkingv1.IngressRule{rule},
		},
	}
}

func mockServiceStatefulSet(name, version string, ready bool, replicas int) *appsv1.StatefulSet {
	return &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			Labels: map[string]string{
				"rollout-group": "mock",
			},
		},
		Spec: appsv1.StatefulSetSpec{
			Replicas: ptr[int32](int32(replicas)),
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"name": name,
				},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Name: name,
					Labels: map[string]string{
						"name":          name,
						"rollout-group": "mock",
					},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "mock",
							Image: "mock-service:latest",
							Ports: []corev1.ContainerPort{
								{ContainerPort: 8080},
							},
							Env: []corev1.EnvVar{
								{Name: "VERSION", Value: version},
								{Name: "READY", Value: fmt.Sprintf("%t", ready)},
								{Name: "PREFIX", Value: pathPrefix(name)},
							},
							LivenessProbe: &corev1.Probe{
								ProbeHandler: corev1.ProbeHandler{
									HTTPGet: &corev1.HTTPGetAction{
										Path: "/alive",
										Port: intstr.FromInt(8080),
									},
								},
								InitialDelaySeconds: 1,
								PeriodSeconds:       1,
							},
							ReadinessProbe: &corev1.Probe{
								ProbeHandler: corev1.ProbeHandler{
									HTTPGet: &corev1.HTTPGetAction{
										Path: "/ready",
										Port: intstr.FromInt(8080),
									},
								},
								InitialDelaySeconds: 1,
								PeriodSeconds:       1,
							},
							ImagePullPolicy: corev1.PullNever,
						},
					},
					RestartPolicy: corev1.RestartPolicyAlways,
				},
			},
			UpdateStrategy: appsv1.StatefulSetUpdateStrategy{
				Type: appsv1.OnDeleteStatefulSetStrategyType,
			},
		},
	}
}

func zoneAwarePodDisruptionBudgetSchema() schema.GroupVersionResource {
	return schema.GroupVersionResource{
		Group:    zpdb.ZoneAwarePodDisruptionBudgetsSpecGroup,
		Version:  zpdb.ZoneAwarePodDisruptionBudgetsVersion,
		Resource: zpdb.ZoneAwarePodDisruptionBudgetsNamePlural, // plural name in CRD
	}
}

func zoneAwarePodDisruptionBudgetSchemaKind() schema.GroupVersionKind {
	return schema.GroupVersionKind{
		Group:   zpdb.ZoneAwarePodDisruptionBudgetsSpecGroup,
		Version: zpdb.ZoneAwarePodDisruptionBudgetsVersion,
		Kind:    zpdb.ZoneAwarePodDisruptionBudgetName,
	}
}

func zoneAwarePodDisruptionBudget(namespace, name, rolloutGroup string, maxUnavailable int64) *unstructured.Unstructured {
	zpdb := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": fmt.Sprintf("%s/%s", zpdb.ZoneAwarePodDisruptionBudgetsSpecGroup, zpdb.ZoneAwarePodDisruptionBudgetsVersion),
			"kind":       zpdb.ZoneAwarePodDisruptionBudgetName,
			"metadata": map[string]interface{}{
				"name":      name,
				"namespace": namespace,
				"labels": map[string]interface{}{
					"name": name,
				},
			},
			"spec": map[string]interface{}{
				zpdb.FieldMaxUnavailable: maxUnavailable,
				zpdb.FieldSelector: map[string]interface{}{
					zpdb.FieldMatchLabels: map[string]interface{}{
						rcfg.RolloutGroupLabelKey: rolloutGroup,
					},
				},
			},
		},
	}

	// because this is an unstructured object we must explicitly set this so the dynamic client can find this resource
	zpdb.SetGroupVersionKind(zoneAwarePodDisruptionBudgetSchemaKind())

	return zpdb
}

func pathPrefix(svcName string) string {
	return "/" + svcName
}

func ptr[T any](t T) *T {
	return &t
}
