package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
	"github.com/pkg/errors"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.uber.org/atomic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/grafana/rollout-operator/pkg/admission"
	"github.com/grafana/rollout-operator/pkg/controller"
	"github.com/grafana/rollout-operator/pkg/tlscert"

	// Required to get the GCP auth provider working.
	_ "k8s.io/client-go/plugin/pkg/client/auth/gcp"
)

func main() {
	// CLI flags.
	serverPort := flag.Int("server.port", 8001, "Port to use for exposing instrumentation and readiness probe endpoints.")
	kubeAPIURL := flag.String("kubernetes.api-url", "", "The Kubernetes server API URL. If not specified, it will be auto-detected when running within a Kubernetes cluster.")
	kubeConfigFile := flag.String("kubernetes.config-file", "", "The Kubernetes config file path. If not specified, it will be auto-detected when running within a Kubernetes cluster.")
	kubeNamespace := flag.String("kubernetes.namespace", "", "The Kubernetes namespace for which this operator is running.")
	reconcileInterval := flag.Duration("reconcile.interval", 5*time.Second, "The minimum interval of reconciliation.")

	serverTLSEnabled := flag.Bool("server-tls.enabled", false, "Enable TLS server for webhook connections.")
	serverTLSPort := flag.Int("server-tls.port", 8443, "Port to use for exposing TLS server for webhook connections (if enabled).")
	serverCertFile := flag.String("server-tls.cert-file", "", "Path to the TLS certificate file if not using the self-signed certificate.")
	serverKeyFile := flag.String("server-tls.key-file", "", "Path to the TLS private key file if not using the self-signed certificate.")

	serverSelfSignedCert := flag.Bool("server-tls.self-signed-cert.enabled", true, "Generate a self-signed certificate for the TLS server.")
	serverSelfSignedCertSecretName := flag.String("server-tls.self-signed-cert.secret-name", "rollout-operator-self-signed-certificate", "Secret name to store the self-signed certificate (if enabled).")
	serverSelfSignedCertDNSName := flag.String("server-tls.self-signed-cert.dns-name", "", "DNS name to use for the self-signed certificate (if enabled). If left empty, then 'rollout-operator.<namespace>.svc' will be used.")
	serverSelfSignedCertOrg := flag.String("server-tls.self-signed-cert.org", "Grafana Labs", "Organization name to use for the self-signed certificate (if enabled).")

	updateWebhooksWithSelfSignedCABundle := flag.Bool("webhooks.update-ca-bundle", true, "Update the CA bundle in the properly labeled webhook configurations with the self-signed certificate (-server-tls.self-signed-cert.enabled should be enabled).")

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
	ready := atomic.NewBool(false)

	// Expose HTTP endpoints.
	srv := newServer(*serverPort, logger)
	srv.Handle("/metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))
	srv.Handle("/ready", http.HandlerFunc(func(res http.ResponseWriter, _ *http.Request) {
		if ready.Load() {
			res.WriteHeader(http.StatusOK)
		} else {
			res.WriteHeader(http.StatusInternalServerError)
		}
	}))

	if err := srv.Start(); err != nil {
		fatal(err)
	}

	// Build the Kubernetes client config.
	cfg, err := buildKubeConfig(*kubeAPIURL, *kubeConfigFile)
	if err != nil {
		fatal(errors.Wrap(err, "failed to build Kubernetes config"))
	}

	kubeClient, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		fatal(errors.Wrap(err, "failed to build Kubernetes client"))
	}

	if *serverTLSEnabled {
		var certProvider tlscert.Provider
		if *serverSelfSignedCert {
			if *serverSelfSignedCertDNSName == "" {
				*serverSelfSignedCertDNSName = fmt.Sprintf("rollout-operator.%s.svc", *kubeNamespace)
			}
			selfSignedProvider := tlscert.NewSelfSignedCertProvider("rollout-operator", []string{*serverSelfSignedCertDNSName}, []string{*serverSelfSignedCertOrg})
			certProvider = tlscert.NewKubeSecretPersistedCertProvider(selfSignedProvider, logger, kubeClient, *kubeNamespace, *serverSelfSignedCertSecretName)
		} else if *serverCertFile != "" && *serverKeyFile != "" {
			certProvider, err = tlscert.NewFileCertProvider(*serverCertFile, *serverKeyFile)
			if err != nil {
				fatal(err)
			}
		} else {
			fatal(errors.New("either self-signed certificate should be enabled or path to the certificate and key should be provided"))
		}

		cert, err := certProvider.Certificate(context.Background())
		if err != nil {
			fatal(err)
		}

		if *updateWebhooksWithSelfSignedCABundle {
			if !*serverSelfSignedCert {
				fatal(errors.New("self-signed certificate should be enabled to update the CA bundle in the webhook configurations"))
			}

			// TODO watch webhook configurations instead of doing one-off.
			err = admission.PatchCABundleOnValidatingWebhooks(context.Background(), logger, kubeClient, *kubeNamespace, cert.CA)
			if err != nil {
				fatal(err)
			}
		}

		tlsSrv, err := newTLSServer(*serverTLSPort, logger, cert)
		if err != nil {
			fatal(err)
		}
		tlsSrv.Handle(admission.NoDownscaleWebhookPath, admission.Serve(admission.NoDownscale, logger))
		if err := tlsSrv.Start(); err != nil {
			fatal(err)
		}
	}

	// Init the controller.
	c := controller.NewRolloutController(kubeClient, *kubeNamespace, *reconcileInterval, reg, logger)
	if err := c.Init(); err != nil {
		fatal(errors.Wrap(err, "error while initialising the controller"))
	}

	// The operator is ready once the controller successfully initialised.
	ready.Store(true)
	c.Run()
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
	fmt.Fprintf(os.Stderr, "%s\n", err.Error())
	os.Exit(1)
}
