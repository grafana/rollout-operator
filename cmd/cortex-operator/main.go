package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
	"github.com/pkg/errors"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/grafana/cortex-operator/pkg/controller"

	// Required to get the GCP auth provider working.
	_ "k8s.io/client-go/plugin/pkg/client/auth/gcp"
)

func main() {
	// CLI flags.
	kubeApiURL := flag.String("kubernetes-api-url", "", "The Kubernetes server API url. If not specified, it will be auto-detected when running within a Kubernetes cluster.")
	kubeConfigFile := flag.String("kubernetes-config-file", "", "The Kubernetes config file path. If not specified, it will be auto-detected when running within a Kubernetes cluster.")
	kubeNamespace := flag.String("kubernetes-namespace", "", "The Kubernetes namespace for which this operator is running.")
	flag.Parse()

	// Validate CLI flags.
	if *kubeNamespace == "" {
		fmt.Fprintf(os.Stderr, "The Kubernetes namespace has not been specified.\n")
		os.Exit(1)
	}
	if (*kubeApiURL == "") != (*kubeConfigFile == "") {
		fmt.Fprintf(os.Stderr, "Either configure both Kubernetes API URL and config file or none of them.\n")
		os.Exit(1)
	}

	logger := log.NewLogfmtLogger(os.Stdout)

	if err := runOperator(*kubeApiURL, *kubeConfigFile, *kubeNamespace, logger); err != nil {
		level.Error(logger).Log("msg", err.Error())
		os.Exit(1)
	}
}

func runOperator(kubeApiURL, kubeConfigFile, kubeNamespace string, logger log.Logger) error {
	cfg, err := buildKubeConfig(kubeApiURL, kubeConfigFile)
	if err != nil {
		return errors.Wrap(err, "failed to build Kubernetes config")
	}

	kubeClient, err := buildKubeClient(cfg)
	if err != nil {
		return errors.Wrap(err, "failed to build Kubernetes client")
	}

	c := controller.NewRolloutController(kubeClient, kubeNamespace, logger)
	if err := c.Init(); err != nil {
		return errors.Wrap(err, "error while initialising the controller")
	}
	c.Run()

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
