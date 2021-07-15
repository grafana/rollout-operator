package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
	"github.com/pkg/errors"
	"github.com/prometheus/client_golang/prometheus"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/grafana/rollout-operator/pkg/controller"
	"github.com/grafana/rollout-operator/pkg/util"

	// Required to get the GCP auth provider working.
	_ "k8s.io/client-go/plugin/pkg/client/auth/gcp"
)

func main() {
	// CLI flags.
	instrumentationPort := flag.Int("instrumentation.server.port", 8001, "Port to use for hosting instrumentation endpoints.")
	kubeAPIURL := flag.String("kubernetes.api-url", "", "The Kubernetes server API URL. If not specified, it will be auto-detected when running within a Kubernetes cluster.")
	kubeConfigFile := flag.String("kubernetes.config-file", "", "The Kubernetes config file path. If not specified, it will be auto-detected when running within a Kubernetes cluster.")
	kubeNamespace := flag.String("kubernetes.namespace", "", "The Kubernetes namespace for which this operator is running.")
	logLevel := flag.String("log.level", "debug", "The log level. Supported values: debug, info, warn, error.")
	flag.Parse()

	// Validate CLI flags.
	if *kubeNamespace == "" {
		fatal(errors.New("The Kubernetes namespace has not been specified."))
	}
	if (*kubeAPIURL == "") != (*kubeConfigFile == "") {
		fatal(errors.New("Either configure both Kubernetes API URL and config file or none of them."))
	}

	logger, err := initLogger(*logLevel)
	if err != nil {
		fatal(err)
	}

	reg := prometheus.NewRegistry()
	instr := util.NewInstrumentationServer(*instrumentationPort, reg, logger)
	if err := instr.Start(); err != nil {
		fatal(err)
	}

	if err := runOperator(*kubeAPIURL, *kubeConfigFile, *kubeNamespace, reg, logger); err != nil {
		fatal(err)
	}
}

func runOperator(kubeAPIURL, kubeConfigFile, kubeNamespace string, reg prometheus.Registerer, logger log.Logger) error {
	cfg, err := buildKubeConfig(kubeAPIURL, kubeConfigFile)
	if err != nil {
		return errors.Wrap(err, "failed to build Kubernetes config")
	}

	kubeClient, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return errors.Wrap(err, "failed to build Kubernetes client")
	}

	c := controller.NewRolloutController(kubeClient, kubeNamespace, reg, logger)
	if err := c.Init(); err != nil {
		return errors.Wrap(err, "error while initialising the controller")
	}
	c.Run()

	return nil
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

func initLogger(minLevel string) (log.Logger, error) {
	logger := log.NewLogfmtLogger(log.NewSyncWriter(os.Stderr))
	var options []level.Option

	switch minLevel {
	case "debug":
		options = append(options, level.AllowDebug())
	case "info":
		options = append(options, level.AllowInfo())
	case "warn":
		options = append(options, level.AllowWarn())
	case "error":
		options = append(options, level.AllowError())
	default:
		return nil, fmt.Errorf("unknown log level: %s", minLevel)
	}

	logger = level.NewFilter(logger, options...)
	logger = log.With(logger, "ts", log.DefaultTimestampUTC)

	return logger, nil
}

func fatal(err error) {
	fmt.Fprintf(os.Stderr, fmt.Sprintf("%s\n", err.Error()))
	os.Exit(1)
}
