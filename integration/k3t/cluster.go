package k3t

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	cliutil "github.com/k3d-io/k3d/v5/cmd/util"
	k3dclient "github.com/k3d-io/k3d/v5/pkg/client"
	"github.com/k3d-io/k3d/v5/pkg/config"
	configtypes "github.com/k3d-io/k3d/v5/pkg/config/types"
	"github.com/k3d-io/k3d/v5/pkg/config/v1alpha5"
	"github.com/k3d-io/k3d/v5/pkg/runtimes"
	k3d "github.com/k3d-io/k3d/v5/pkg/types"
	"github.com/k3d-io/k3d/v5/version"
	"github.com/stretchr/testify/require"
	apiextensionsclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

func init() {
	version.Version = "v5.4.6"

	if path := os.Getenv("K3T_DEBUG_KUBECONFIG_PATH"); path != "" {
		DebugKubeconfigPath = path
	}
}

var DebugKubeconfigPath = "/tmp/k3t/kubeconfig"

// WithName returns an option that sets a defined cluster name on creation.
func WithName(name string) Option { return nameOption(name) }

// WithImages returns an option that preloads the specified images into the cluster.
// It expects those images to be already present on the host.
// Can be specified multiple times.
func WithImages(images ...string) Option { return imagesOption(images) }

// WithNoLoggerOverride disables the k3d logger override to the testing.T.Logf().
// This is useful if multiple tests are ran in parallel, as k3d logger is one global one.
func WithNoLoggerOverride() Option { return noLoggerOverrideOption{} }

// WithLoadBalancerPort overrides the load balancer port exposed on the host machine, instead of using a random free one.
func WithLoadBalancerPort(port string) Option { return loadBalancerPortOption(port) }

// WithAPIPort overrides the Kubernetes API port exposed on the host machine, instead of using a random free one.
func WithAPIPort(port string) Option { return apiPortOption(port) }

func NewCluster(ctx context.Context, t *testing.T, opts ...Option) Cluster {
	opt := options{
		clusterName:    testClusterName(),
		overrideLogger: true,
		lbPort:         getFreePort(t, "8080"),
		apiPort:        getFreePort(t, k3d.DefaultAPIPort),
	}
	for _, o := range opts {
		o.configure(&opt)
	}

	if opt.overrideLogger {
		ConfigureLogger(t)
	}

	// Prepare the cluster config.
	simpleCfg := v1alpha5.SimpleConfig{
		TypeMeta: configtypes.TypeMeta{},
		ObjectMeta: configtypes.ObjectMeta{
			Name: opt.clusterName,
		},
		Servers: 1,
		Agents:  0,
		ExposeAPI: v1alpha5.SimpleExposureOpts{
			HostPort: opt.apiPort,
		},
		Ports: []v1alpha5.PortWithNodeFilters{
			{Port: fmt.Sprintf("%s:80", opt.lbPort), NodeFilters: []string{"loadbalancer"}},
		},
		Image: fmt.Sprintf("%s:%s", k3d.DefaultK3sImageRepo, version.K3sVersion),
		Options: v1alpha5.SimpleConfigOptions{
			K3dOptions: v1alpha5.SimpleConfigOptionsK3d{
				Wait: true, // Wait for server node.
			},
		},
	}

	clusterConfig, err := config.TransformSimpleToClusterConfig(ctx, runtimes.SelectedRuntime, simpleCfg, "")
	require.NoError(t, err, "Can't transform simple config to cluster config")

	clusterConfig, err = config.ProcessClusterConfig(*clusterConfig)
	require.NoError(t, err, "Failed to process cluster config")

	err = config.ValidateClusterConfig(ctx, runtimes.SelectedRuntime, *clusterConfig)
	require.NoError(t, err, "Failed to validate cluster config")

	// Check existing cluster.
	_, err = k3dclient.ClusterGet(ctx, runtimes.SelectedRuntime, &clusterConfig.Cluster)
	require.Errorf(t, err, "Cluster %s already exists, remove it running `k3d cluster delete %s`, or by just removing the corresponding docker containers.", clusterConfig.Name, clusterConfig.Name)

	// Prepare cleanup.
	t.Cleanup(func() {
		err := k3dclient.ClusterDelete(ctx, runtimes.SelectedRuntime, &clusterConfig.Cluster, k3d.ClusterDeleteOpts{SkipRegistryCheck: true})
		require.NoError(t, err, "Can't delete cluster on cleanup.")
	})

	// Create the cluster.
	err = k3dclient.ClusterRun(ctx, runtimes.SelectedRuntime, clusterConfig)
	if err != nil && strings.Contains(err.Error(), "could not find an available, non-overlapping IPv4 address pool among the defaults to assign to the network") {
		t.Logf("Hint: try running `docker network prune`")
	}
	require.NoError(t, err, "Failed creating cluster.")
	t.Logf("Cluster '%s' created successfully!", clusterConfig.Name)

	// Get kubeconfig and instantiate kubernetes.Clientset.
	kubeConfigFile := filepath.Join(t.TempDir(), fmt.Sprintf("kubeconfig.%s.yaml", opt.clusterName))

	kubeconfig, err := k3dclient.KubeconfigGet(ctx, runtimes.SelectedRuntime, &clusterConfig.Cluster)
	require.NoError(t, err, "Failed to get kubeconfig from cluster")

	err = clientcmd.WriteToFile(*kubeconfig, kubeConfigFile)
	require.NoError(t, err, "Failed to write kubeconfig to file")

	t.Logf("Writing KUBECONFIG to %s", DebugKubeconfigPath)
	if err := clientcmd.WriteToFile(*kubeconfig, DebugKubeconfigPath); err != nil {
		t.Logf("Ignoring error while writing KUBECONFIG to %s: %s", DebugKubeconfigPath, err)
	}

	config, err := clientcmd.BuildConfigFromFlags("", kubeConfigFile)
	require.NoError(t, err, "Failed to build config from flags")

	clientset, err := kubernetes.NewForConfig(config)
	require.NoError(t, err, "Failed to build kubernetes clientset")

	apiExtClient, err := apiextensionsclient.NewForConfig(config)
	require.NoError(t, err, "Failed to build apiextensions clientset")

	apiDynClient, err := dynamic.NewForConfig(config)
	require.NoError(t, err, "Failed to build dynamic client")

	t.Logf("KUBECONFIG=%s", kubeConfigFile)

	if len(opt.images) > 0 {
		t.Logf("Preloading images: %v", opt.images)
		loadImages(ctx, t, opt.images, &clusterConfig.Cluster)
	}

	return Cluster{
		k:             clientset,
		extK:          apiExtClient,
		dynK:          apiDynClient,
		clusterConfig: clusterConfig,
		lbPort:        opt.lbPort,
	}
}

func testClusterName() string {
	return fmt.Sprintf("k3d-test-%d", time.Now().UnixMilli())
}

func getFreePort(t *testing.T, defaultPort string) string {
	port, err := cliutil.GetFreePort()
	hostAPIPort := strconv.Itoa(port)
	if err != nil || port == 0 {
		t.Logf("Failed to get random free port: %+v", err)
		t.Logf("Falling back to internal port %s (may be blocked though)...", k3d.DefaultAPIPort)
		return defaultPort
	}
	return hostAPIPort
}

type Cluster struct {
	clusterConfig *v1alpha5.ClusterConfig
	k             *kubernetes.Clientset
	extK          *apiextensionsclient.Clientset
	dynK          *dynamic.DynamicClient
	lbPort        string
}

func (c Cluster) API() *kubernetes.Clientset {
	return c.k
}
func (c Cluster) ExtAPI() *apiextensionsclient.Clientset { return c.extK }
func (c Cluster) DynK() *dynamic.DynamicClient           { return c.dynK }
func (c Cluster) LBPort() string {
	return c.lbPort
}

func loadImages(ctx context.Context, t *testing.T, images []string, cluster *k3d.Cluster) {
	// TODO: we're using k3d.ImportModeToolsNode mode because the auto-detect (that uses pipe) fails in CI: https://github.com/k3d-io/k3d/issues/900
	loadImageOpts := k3d.ImageImportOpts{Mode: k3d.ImportModeToolsNode}
	err := k3dclient.ImageImportIntoClusterMulti(ctx, runtimes.SelectedRuntime, images, cluster, loadImageOpts)
	require.NoError(t, err, "Failed to load images.")
}

type options struct {
	clusterName    string
	images         []string
	overrideLogger bool
	lbPort         string
	apiPort        string
}

// Option configures a cluster creation.
// It intentionally contains unexported methods to make sure that the contract of Option can be changed without breaking changes.
// We're using an interface with specific types for each option because it's easier to debug these kind of options later,
// compared to a list of anonymous functions.
type Option interface {
	configure(cfg *options)
}

type nameOption string

func (n nameOption) configure(opts *options) { opts.clusterName = string(n) }

type imagesOption []string

func (imgs imagesOption) configure(opts *options) {
	opts.images = append(opts.images, []string(imgs)...)
}

type noLoggerOverrideOption struct{}

func (noLoggerOverrideOption) configure(opts *options) { opts.overrideLogger = false }

type loadBalancerPortOption string

func (p loadBalancerPortOption) configure(opts *options) { opts.lbPort = string(p) }

type apiPortOption string

func (p apiPortOption) configure(opts *options) { opts.apiPort = string(p) }
