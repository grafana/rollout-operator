//go:build requires_docker

package integration

import (
	"context"
	"testing"

	"github.com/grafana/e2e"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/grafana/rollout-operator/integration/k3d"
)

const clusterName = "k3s-default"
const networkName = "rollout-operator-tests"

func TestK3D(t *testing.T) {
	s, err := e2e.NewScenario(networkName)
	require.NoError(t, err)
	t.Cleanup(s.Close)

	k, err := k3d.New(k3d.DefaultK3dImage)
	require.NoError(t, err)
	require.NoError(t, s.Start(k))
	require.NoError(t, s.WaitReady(k))
	require.NoError(t, k.PreloadDefaultImages())

	clientset, err := k.CreateCluster(clusterName)
	require.NoError(t, err)

	ns, err := clientset.CoreV1().Namespaces().Create(context.Background(), &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: "test-ns"},
	}, metav1.CreateOptions{})

	require.NoError(t, err, "Can't create namespace")
	t.Logf("Namespace: %+v", *ns)
}
