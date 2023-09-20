//go:build requires_docker

package integration

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/grafana/rollout-operator/integration/k3t"
)

// requireEventuallyPod runs the tests provided on the pod retrieved from the API.
// Once a test succeeds, it's not evaluated again.
func requireEventuallyPod(t *testing.T, api *kubernetes.Clientset, ctx context.Context, podName string, tests ...func(t *testing.T, pod *corev1.Pod) bool) {
	ok := 0
	require.Eventuallyf(t, func() bool {
		pod, err := api.CoreV1().Pods(corev1.NamespaceDefault).Get(ctx, podName, metav1.GetOptions{})
		if err != nil {
			t.Logf("Can't get pod %s: %s", podName, err)
			return false
		}
		for _, test := range tests[ok:] {
			if !test(t, pod) {
				return false
			}
			ok++
		}
		return true
	}, 5*time.Minute, 500*time.Millisecond, "Pod %s did not match the expected condition", podName)
}

func expectPodPhase(expectedPhase corev1.PodPhase) func(t *testing.T, pod *corev1.Pod) bool {
	return func(t *testing.T, pod *corev1.Pod) bool {
		phase := pod.Status.Phase
		if phase == expectedPhase {
			t.Logf("Pod %q phase is the expected %q", pod.Name, phase)
			return true
		}
		t.Logf("Pod %q phase %q is not the expected %q.", pod.Name, phase, expectedPhase)
		return false
	}
}

func expectReady() func(t *testing.T, pod *corev1.Pod) bool {
	return expectedPodReadyState(true)
}

func expectNotReady() func(t *testing.T, pod *corev1.Pod) bool {
	return expectedPodReadyState(false)
}

func expectVersion(expectedVersion string) func(t *testing.T, pod *corev1.Pod) bool {
	return func(t *testing.T, pod *corev1.Pod) bool {
		for _, c := range pod.Spec.Containers {
			for _, env := range c.Env {
				if env.Name == "VERSION" {
					if env.Value == expectedVersion {
						t.Logf("Pod %s container %s has env VERSION=%q as expected.", pod.Name, c.Name, env.Value)
						return true
					}
					t.Logf("Pod %s container %s has env VERSION=%q, but expected VERSION=%q.", pod.Name, c.Name, env.Value, expectedVersion)
					return false
				}
			}
		}
		t.Logf("No container had VERSION env var for pod %s", pod.Name)
		return false
	}
}

func expectedPodReadyState(expectedReady bool) func(t *testing.T, pod *corev1.Pod) bool {
	return func(t *testing.T, pod *corev1.Pod) bool {
		if len(pod.Status.ContainerStatuses) == 0 {
			t.Logf("No container statuses defined for pod %s", pod.Name)
			return false
		}
		s := pod.Status.ContainerStatuses[0]
		if s.Ready == expectedReady {
			t.Logf("Pod %s container %s is ready=%t as expected.", pod.Name, s.Name, s.Ready)
			return true
		}
		t.Logf("Pod %s container %s is ready=%t, but expected %t.", pod.Name, s.Name, s.Ready, expectedReady)
		return false
	}
}

func requireEventuallyPodCount(ctx context.Context, t *testing.T, api *kubernetes.Clientset, selector string, expectedCount int) {
	require.Eventually(t, func() bool {
		l, err := api.CoreV1().Pods(corev1.NamespaceDefault).List(ctx, metav1.ListOptions{LabelSelector: selector})
		if err != nil {
			t.Logf("Can't list pods matching %s: %s", selector, err)
			return false
		}

		if len(l.Items) != expectedCount {
			t.Logf("Expected pod count %d for %s, got %d", expectedCount, selector, len(l.Items))
			return false
		}

		t.Logf("Got exactly %d pods for %s", expectedCount, selector)
		return true
	}, 5*time.Minute, 500*time.Millisecond)
}

func eventuallyGetFirstPod(ctx context.Context, t *testing.T, api *kubernetes.Clientset, selector string) string {
	var podName string
	require.Eventuallyf(t, func() bool {
		l, err := api.CoreV1().Pods(corev1.NamespaceDefault).List(ctx, metav1.ListOptions{LabelSelector: selector})
		if err != nil {
			t.Logf("Can't list pods matching %s: %s", selector, err)
			return false
		}
		if len(l.Items) == 0 {
			t.Logf("No pods for %s yet", selector)
			return false
		}
		podName = l.Items[0].Name
		t.Logf("Found %s pod %s", selector, podName)
		return true
	}, 5*time.Minute, 500*time.Millisecond, "Could not find pods matching %s", selector)
	return podName
}

// makeMockReady sends a POST request to the /ready endpoint of the mock service, to make it become ready
func makeMockReady(t *testing.T, cluster k3t.Cluster, svc string) {
	lbAddr := fmt.Sprintf("127.0.0.1:%s", cluster.LBPort())
	require.Eventuallyf(t, func() bool {
		uri := fmt.Sprintf("http://%s/%s/ready", lbAddr, svc)
		resp, err := http.Post(uri, "text/plain", nil)
		if err != nil {
			t.Logf("POST %s: %s", uri, err)
			return false
		}
		defer resp.Body.Close()

		t.Logf("POST %s: returned status code %d", uri, resp.StatusCode)
		return resp.StatusCode == http.StatusOK
	}, 1*time.Minute, 500*time.Millisecond, "Never got the expected version from %s", svc)
}

func requireCreateStatefulSet(ctx context.Context, t *testing.T, api *kubernetes.Clientset, sts *appsv1.StatefulSet) {
	t.Helper()
	_, err := api.AppsV1().StatefulSets(corev1.NamespaceDefault).Create(ctx, sts, metav1.CreateOptions{})
	require.NoError(t, err, "Can't create StatefulSet")
}

func requireUpdateStatefulSet(ctx context.Context, t *testing.T, api *kubernetes.Clientset, sts *appsv1.StatefulSet) {
	t.Helper()
	_, err := api.AppsV1().StatefulSets(corev1.NamespaceDefault).Update(ctx, sts, metav1.UpdateOptions{})
	require.NoError(t, err, "Can't update StatefulSet")
}

func getAndUpdateStatefulSetScale(ctx context.Context, t *testing.T, api *kubernetes.Clientset, name string, replicas int32, dryrun bool) error {
	s, err := api.AppsV1().StatefulSets(corev1.NamespaceDefault).GetScale(ctx, name, metav1.GetOptions{})
	require.NoError(t, err)
	s.Spec.Replicas = replicas
	opts := metav1.UpdateOptions{}
	if dryrun {
		opts.DryRun = []string{metav1.DryRunAll}
	}
	_, err = api.AppsV1().StatefulSets(corev1.NamespaceDefault).UpdateScale(ctx, name, s, opts)
	return err
}
