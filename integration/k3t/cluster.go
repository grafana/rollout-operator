package k3t

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"testing"

	cliutil "github.com/k3d-io/k3d/v5/cmd/util"
	k3dclient "github.com/k3d-io/k3d/v5/pkg/client"
	"github.com/k3d-io/k3d/v5/pkg/config"
	configtypes "github.com/k3d-io/k3d/v5/pkg/config/types"
	"github.com/k3d-io/k3d/v5/pkg/runtimes"
	k3d "github.com/k3d-io/k3d/v5/pkg/types"
	"github.com/k3d-io/k3d/v5/version"
	"github.com/stretchr/testify/require"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/k3d-io/k3d/v5/pkg/config/v1alpha4"
)

func init() {
	version.Version = "v5.4.0"
}

func NewCluster(ctx context.Context, t *testing.T, clusterName string) Cluster {
	lbPort := getFreePort(t, "8080")

	// Prepare the cluster config.
	simpleCfg := v1alpha4.SimpleConfig{
		TypeMeta: configtypes.TypeMeta{},
		ObjectMeta: configtypes.ObjectMeta{
			Name: clusterName,
		},
		Servers: 1,
		Agents:  0,
		ExposeAPI: v1alpha4.SimpleExposureOpts{
			HostPort: getFreePort(t, k3d.DefaultAPIPort),
		},
		Ports: []v1alpha4.PortWithNodeFilters{
			{Port: fmt.Sprintf("%s:80", lbPort), NodeFilters: []string{"loadbalancer"}},
		},
		Image: fmt.Sprintf("%s:%s", k3d.DefaultK3sImageRepo, version.K3sVersion),
		Options: v1alpha4.SimpleConfigOptions{
			K3dOptions: v1alpha4.SimpleConfigOptionsK3d{
				Wait: true, // Wait for server node.
			},
		},
	}

	clusterConfig, err := config.TransformSimpleToClusterConfig(ctx, runtimes.SelectedRuntime, simpleCfg)
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
	require.NoError(t, err, "Failed creating cluster.")
	t.Logf("Cluster '%s' created successfully!", clusterConfig.Cluster.Name)

	// Get kubeconfig and instantiate kubernetes.Clientset.
	kubeConfigFile := filepath.Join(t.TempDir(), fmt.Sprintf("kubeconfig.%s.yaml", clusterName))

	kubeconfig, err := k3dclient.KubeconfigGet(ctx, runtimes.SelectedRuntime, &clusterConfig.Cluster)
	require.NoError(t, err, "Failed to get kubeconfig from cluster")

	err = clientcmd.WriteToFile(*kubeconfig, kubeConfigFile)
	require.NoError(t, err, "Failed to write kubeconfig to file")

	config, err := clientcmd.BuildConfigFromFlags("", kubeConfigFile)
	require.NoError(t, err, "Failed to build config from flags")

	clientset, err := kubernetes.NewForConfig(config)
	require.NoError(t, err, "Failed to build kubernetes clientset")

	t.Logf("KUBECONFIG=%s", kubeConfigFile)

	return Cluster{
		k:             clientset,
		clusterConfig: clusterConfig,
		lbPort:        lbPort,
	}
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
	clusterConfig *v1alpha4.ClusterConfig
	k             *kubernetes.Clientset
	lbPort        string
}

func (c Cluster) API() *kubernetes.Clientset {
	return c.k
}

func (c Cluster) LBPort() string {
	return c.lbPort
}

func (c Cluster) LoadImages(ctx context.Context, t *testing.T, images ...string) {
	t.Helper()
	loadImageOpts := k3d.ImageImportOpts{Mode: k3d.ImportModeAutoDetect}
	err := k3dclient.ImageImportIntoClusterMulti(ctx, runtimes.SelectedRuntime, images, &c.clusterConfig.Cluster, loadImageOpts)
	require.NoError(t, err, "Failed to load images.")
}
