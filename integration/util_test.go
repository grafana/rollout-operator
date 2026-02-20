//go:build requires_docker

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	autoscalingv1 "k8s.io/api/autoscaling/v1"
	corev1 "k8s.io/api/core/v1"
	apiextensionsclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

const (
	kindImage            string = "kindest/node:v1.35.1"
	nodePortMockServiceA int32  = 30080 + iota
	nodePortMockServiceB
	nodePortMockServiceC
)

var (
	// Stores kind cluster names possibly created so they can be cleaned up upon interrupt
	activeClusterNames = make(map[string]struct{})
	activeClustersMu   sync.Mutex
)

func TestMain(m *testing.M) {
	// Prevents clusters from always being leaked on Ctrl+C.
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigChan
		// Note: This may race an in-progress kind cluster create. We could hold the lock during cluster creation to prevent that race,
		// but doing that could stall the interrupt for a possibly a lengthy amount of time.
		fmt.Println("\nReceived interrupt signal, cleaning up kind clusters. If you interrupted during cluster creation this may race that.")
		// Lock and hold until exit to prevent new clusters from being registered
		activeClustersMu.Lock()
		for clusterName := range activeClusterNames {
			cleanupCluster(clusterName)
		}
		os.Exit(1)
	}()

	os.Exit(m.Run())
}

// cleanupCluster deletes a kind cluster by name. Safe to call multiple times (idempotent).
func cleanupCluster(clusterName string) {
	fmt.Printf("Deleting kind cluster: %s\n", clusterName)
	cmd := exec.Command("kind", "delete", "cluster", "--name", clusterName)
	if err := cmd.Run(); err != nil {
		fmt.Printf("Warning: failed to delete cluster %s: %v\n", clusterName, err)
	}
}

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
func makeMockReady(t *testing.T, cluster *kindCluster, svc string) {
	hostPort, err := cluster.ServicePort(svc)
	require.NoError(t, err, "Failed to get service port for %s", svc)
	t.Logf("Making %s ready via port %d", svc, hostPort)

	// Wait for service endpoints to be updated
	ctx := context.Background()
	require.Eventuallyf(t, func() bool {
		// List all EndpointSlices for this service
		endpointSlices, err := cluster.API().DiscoveryV1().EndpointSlices(corev1.NamespaceDefault).List(ctx, metav1.ListOptions{
			LabelSelector: fmt.Sprintf("kubernetes.io/service-name=%s", svc),
		})
		if err != nil {
			t.Logf("Failed to get endpoint slices for %s: %v", svc, err)
			return false
		}
		// Check if any EndpointSlice has endpoints
		for _, slice := range endpointSlices.Items {
			if len(slice.Endpoints) > 0 {
				t.Logf("Service %s has %d endpoint(s) in slice %s", svc, len(slice.Endpoints), slice.Name)
				return true
			}
		}
		t.Logf("Service %s has no endpoints yet", svc)
		return false
	}, 30*time.Second, 500*time.Millisecond, "Service %s never got endpoints", svc)

	require.Eventuallyf(t, func() bool {
		uri := fmt.Sprintf("http://127.0.0.1:%d/ready", hostPort)
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

func patchStatefulSetScale(ctx context.Context, t *testing.T, api *kubernetes.Clientset, name string, replicas int32, dryrun bool) error {
	scale := &autoscalingv1.Scale{
		Spec: autoscalingv1.ScaleSpec{
			Replicas: replicas,
		},
	}
	patchData, err := json.Marshal(scale)
	require.NoError(t, err)

	opts := metav1.PatchOptions{}
	if dryrun {
		opts.DryRun = []string{metav1.DryRunAll}
	}
	_, err = api.AppsV1().StatefulSets(corev1.NamespaceDefault).Patch(ctx, name, types.MergePatchType, patchData, opts, "scale")
	return err
}

// kindCluster represents a kind cluster for testing
type kindCluster struct {
	name        string
	clientset   *kubernetes.Clientset
	extAPI      *apiextensionsclient.Clientset
	dynAPI      *dynamic.DynamicClient
	config      *rest.Config
	portMapping map[int32]int // node port -> host port
}

// createKindCluster creates a new kind cluster for testing
func createKindCluster(t *testing.T, images ...string) *kindCluster {
	t.Helper()

	// Generate unique cluster name
	clusterName := fmt.Sprintf("test-rollout-operator-%d", time.Now().UnixNano())

	freePorts := getFreePorts(t, 3)
	nodePortMapping := map[int32]int{
		nodePortMockServiceA: freePorts[0],
		nodePortMockServiceB: freePorts[1],
		nodePortMockServiceC: freePorts[2],
	}

	kindConfig := fmt.Sprintf(`kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
nodes:
- role: control-plane
  extraPortMappings:
  - containerPort: %d
    hostPort: %d
    listenAddress: "127.0.0.1"
    protocol: TCP
  - containerPort: %d
    hostPort: %d
    listenAddress: "127.0.0.1"
    protocol: TCP
  - containerPort: %d
    hostPort: %d
    listenAddress: "127.0.0.1"
    protocol: TCP
`,
		nodePortMockServiceA, nodePortMapping[nodePortMockServiceA],
		nodePortMockServiceB, nodePortMapping[nodePortMockServiceB],
		nodePortMockServiceC, nodePortMapping[nodePortMockServiceC])

	configPath := filepath.Join(t.TempDir(), "kind-config.yaml")
	err := os.WriteFile(configPath, []byte(kindConfig), 0600)
	require.NoError(t, err, "Failed to write kind config")

	// Prepare kubeconfig path
	kubeconfigPath := filepath.Join(t.TempDir(), "kubeconfig")
	t.Logf("using kubeconfig path: %s", kubeconfigPath)

	// Register cluster name before creation so signal handler in TestMain knows it might exist
	activeClustersMu.Lock()
	activeClusterNames[clusterName] = struct{}{}
	activeClustersMu.Unlock()

	// Create kind cluster with config
	t.Logf("Creating kind cluster: %s", clusterName)
	cmd := exec.Command("kind", "create", "cluster",
		"--name", clusterName,
		"--config", configPath,
		"--kubeconfig", kubeconfigPath,
		"--wait", "5m",
		"--image", kindImage)

	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Logf("kind create output: %s", string(output))
	}
	require.NoError(t, err, "Failed to create kind cluster")

	// Build Kubernetes clients
	config, err := clientcmd.BuildConfigFromFlags("", kubeconfigPath)
	require.NoError(t, err, "Failed to build config")

	clientset, err := kubernetes.NewForConfig(config)
	require.NoError(t, err, "Failed to create clientset")

	extAPI, err := apiextensionsclient.NewForConfig(config)
	require.NoError(t, err, "Failed to create apiextensions clientset")

	dynAPI, err := dynamic.NewForConfig(config)
	require.NoError(t, err, "Failed to create dynamic clientset")

	// Load images into cluster
	for _, image := range images {
		t.Logf("Loading image into cluster: %s", image)
		cmd = exec.Command("kind", "load", "docker-image", image, "--name", clusterName)
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Logf("kind load output: %s", string(output))
			require.NoError(t, err, "Failed to load image %s", image)
		}
	}

	cluster := &kindCluster{
		name:        clusterName,
		clientset:   clientset,
		extAPI:      extAPI,
		dynAPI:      dynAPI,
		config:      config,
		portMapping: nodePortMapping,
	}

	t.Cleanup(func() {
		cleanupCluster(clusterName)

		activeClustersMu.Lock()
		delete(activeClusterNames, clusterName)
		activeClustersMu.Unlock()
	})

	return cluster
}

// API returns the Kubernetes clientset
func (c *kindCluster) API() *kubernetes.Clientset {
	return c.clientset
}

// ExtAPI returns the API extensions clientset
func (c *kindCluster) ExtAPI() *apiextensionsclient.Clientset {
	return c.extAPI
}

// DynK returns the dynamic client
func (c *kindCluster) DynK() *dynamic.DynamicClient {
	return c.dynAPI
}

// Config returns the rest.Config
func (c *kindCluster) Config() *rest.Config {
	return c.config
}

// ServicePort returns the host port for a given service name
func (c *kindCluster) ServicePort(serviceName string) (int, error) {
	nodePort, err := serviceNameToNodePort(serviceName)
	if err != nil {
		return 0, err
	}

	hostPort, ok := c.portMapping[nodePort]
	if !ok {
		return 0, fmt.Errorf("no host port mapping for NodePort %d", nodePort)
	}
	return hostPort, nil
}

// getFreePorts returns n unique free ports by opening n listeners simultaneously
func getFreePorts(t *testing.T, n int) []int {
	t.Helper()

	ports := make([]int, n)

	for i := range n {
		// port 0 means select any available port
		listener, err := net.Listen("tcp", ":0")
		require.NoError(t, err, "Failed to get random free port")
		defer listener.Close()                         // defer because we don't want to get the same port twice
		ports[i] = listener.Addr().(*net.TCPAddr).Port // extracts the port
	}

	return ports
}
