//go:build requires_docker

package integration

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"
)

func createMockServiceZone(t *testing.T, ctx context.Context, api *kubernetes.Clientset, namespace, name string) {
	t.Helper()
	{
		_, err := api.AppsV1().StatefulSets(namespace).Create(ctx, mockServiceStatefulSet(name, "1", true), metav1.CreateOptions{})
		require.NoError(t, err, "Can't create StatefulSet")
	}

	{
		svc, err := mockServiceService(name)
		require.NoError(t, err, "Can't create mock service manifest")
		_, err = api.CoreV1().Services(namespace).Create(ctx, svc, metav1.CreateOptions{})
		require.NoError(t, err, "Can't create Service")
	}
}

func mockServiceService(name string) (*corev1.Service, error) {
	// Assign NodePort based on service name to match kind cluster port mappings
	nodePort, err := serviceNameToNodePort(name)
	if err != nil {
		return nil, err
	}

	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: corev1.ServiceSpec{
			Type: corev1.ServiceTypeNodePort,
			Selector: map[string]string{
				"name": name,
			},
			Ports: []corev1.ServicePort{
				{
					Name:       "http",
					Protocol:   corev1.ProtocolTCP,
					Port:       8080,
					TargetPort: intstr.FromInt(8080),
					NodePort:   nodePort,
				},
			},
			PublishNotReadyAddresses: true, // We want to control them even if they're not ready.
		},
	}, nil
}

func mockServiceStatefulSet(name, version string, ready bool) *appsv1.StatefulSet {
	return &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			Labels: map[string]string{
				"rollout-group": "mock",
			},
		},
		Spec: appsv1.StatefulSetSpec{
			Replicas: ptr[int32](1),
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"name": name,
				},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Name: name,
					Labels: map[string]string{
						"name": name,
					},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:            "mock",
							Image:           "mock-service:latest",
							ImagePullPolicy: corev1.PullNever,
							Ports: []corev1.ContainerPort{
								{ContainerPort: 8080},
							},
							Env: []corev1.EnvVar{
								{Name: "VERSION", Value: version},
								{Name: "READY", Value: fmt.Sprintf("%t", ready)},
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

// serviceNameToNodePort maps service names to their assigned NodePorts
func serviceNameToNodePort(name string) (int32, error) {
	switch name {
	case "mock-zone-a":
		return nodePortMockServiceA, nil
	case "mock-zone-b":
		return nodePortMockServiceB, nil
	case "mock-zone-c":
		return nodePortMockServiceC, nil
	default:
		return 0, fmt.Errorf("unknown service name: %s", name)
	}
}

func ptr[T any](t T) *T {
	return &t
}
