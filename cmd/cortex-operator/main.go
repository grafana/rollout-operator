package main

import (
	"os"
	"time"

	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
	"github.com/pkg/errors"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/grafana/cortex-operator/pkg/controller"

	// Required to get the GCP auth provider working.
	_ "k8s.io/client-go/plugin/pkg/client/auth/gcp"
)

const (
	// How frequently informers should resync. This is also the frequency at which
	// the operator reconciles even if no changes are made to the watched resources.
	// TODO increase it
	informerSyncInterval = 10 * time.Second
)

func main() {
	logger := log.NewLogfmtLogger(os.Stdout)

	if err := run(logger); err != nil {
		level.Error(logger).Log("msg", err.Error())
		os.Exit(1)
	}
}

func run(logger log.Logger) error {
	// TODO allow to configure it
	masterURL := "https://35.239.147.248"
	kubeCfgFile := "/Users/marco/.kube/config"
	namespace := "cortex-dev-04"

	cfg, err := buildKubeConfig(masterURL, kubeCfgFile)
	if err != nil {
		return errors.Wrap(err, "failed to build Kubernetes config")
	}

	client, err := buildKubeClient(cfg)
	if err != nil {
		return errors.Wrap(err, "failed to build Kubernetes client")
	}

	informerFactory := informers.NewSharedInformerFactoryWithOptions(client, informerSyncInterval, informers.WithNamespace(namespace))

	ctrl := controller.NewController(client, namespace, informerFactory, logger)
	if err := ctrl.Run(); err != nil {
		return errors.Wrap(err, "error while running controller")
	}

	return nil
}

func buildKubeClient(cfg *rest.Config) (*kubernetes.Clientset, error) {
	return kubernetes.NewForConfig(cfg)
}

func buildKubeConfig(apiURL, cfgFile string) (*rest.Config, error) {
	if cfgFile != "" {
		config, err := clientcmd.BuildConfigFromFlags(apiURL, cfgFile)
		if err != nil {
			return nil, err
		}
		return config, nil
	}

	return rest.InClusterConfig()
}
