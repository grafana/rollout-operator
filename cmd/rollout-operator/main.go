package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
	"github.com/pkg/errors"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/prometheus/common/model"
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

type config struct {
	logLevel string

	serverPort        int
	kubeAPIURL        string
	kubeConfigFile    string
	kubeNamespace     string
	reconcileInterval time.Duration

	serverTLSEnabled bool
	serverTLSPort    int
	serverCertFile   string
	serverKeyFile    string

	serverSelfSignedCert                 bool
	serverSelfSignedCertSecretName       string
	serverSelfSignedCertDNSName          string
	serverSelfSignedCertOrg              string
	serverSelfSignedCertExpirationString string

	updateWebhooksWithSelfSignedCABundle bool
}

func main() {
	// CLI flags.
	var cfg config
	flag.StringVar(&cfg.logLevel, "log.level", "debug", "The log level. Supported values: debug, info, warn, error.")
	flag.IntVar(&cfg.serverPort, "server.port", 8001, "Port to use for exposing instrumentation and readiness probe endpoints.")
	flag.StringVar(&cfg.kubeAPIURL, "kubernetes.api-url", "", "The Kubernetes server API URL. If not specified, it will be auto-detected when running within a Kubernetes cluster.")
	flag.StringVar(&cfg.kubeConfigFile, "kubernetes.config-file", "", "The Kubernetes config file path. If not specified, it will be auto-detected when running within a Kubernetes cluster.")
	flag.StringVar(&cfg.kubeNamespace, "kubernetes.namespace", "", "The Kubernetes namespace for which this operator is running.")
	flag.DurationVar(&cfg.reconcileInterval, "reconcile.interval", 5*time.Second, "The minimum interval of reconciliation.")

	flag.BoolVar(&cfg.serverTLSEnabled, "server-tls.enabled", false, "Enable TLS server for webhook connections.")
	flag.IntVar(&cfg.serverTLSPort, "server-tls.port", 8443, "Port to use for exposing TLS server for webhook connections (if enabled).")
	flag.StringVar(&cfg.serverCertFile, "server-tls.cert-file", "", "Path to the TLS certificate file if not using the self-signed certificate.")
	flag.StringVar(&cfg.serverKeyFile, "server-tls.key-file", "", "Path to the TLS private key file if not using the self-signed certificate.")

	flag.BoolVar(&cfg.serverSelfSignedCert, "server-tls.self-signed-cert.enabled", true, "Generate a self-signed certificate for the TLS server.")
	flag.StringVar(&cfg.serverSelfSignedCertSecretName, "server-tls.self-signed-cert.secret-name", "rollout-operator-self-signed-certificate", "Secret name to store the self-signed certificate (if enabled).")
	flag.StringVar(&cfg.serverSelfSignedCertDNSName, "server-tls.self-signed-cert.dns-name", "", "DNS name to use for the self-signed certificate (if enabled). If left empty, then 'rollout-operator.<namespace>.svc' will be used.")
	flag.StringVar(&cfg.serverSelfSignedCertOrg, "server-tls.self-signed-cert.org", "Grafana Labs", "Organization name to use for the self-signed certificate (if enabled).")
	flag.StringVar(&cfg.serverSelfSignedCertExpirationString, "server-tls.self-signed-cert.expiration", "1y", "Expiration time for the self-signed certificate in Prometheus duration format (Go format plus support for days, weeks and years as 1d/1w/1y).")

	flag.BoolVar(&cfg.updateWebhooksWithSelfSignedCABundle, "webhooks.update-ca-bundle", true, "Update the CA bundle in the properly labeled webhook configurations with the self-signed certificate (-server-tls.self-signed-cert.enabled should be enabled).")

	flag.Parse()

	var serverSelfSignedCertExpiration time.Duration
	if cfg.serverSelfSignedCert {
		serverSelfSignedCertExpirationPromDuration, err := model.ParseDuration(cfg.serverSelfSignedCertExpirationString)
		if err != nil {
			fatal(fmt.Errorf("can't parse "))
		}
		serverSelfSignedCertExpiration = time.Duration(serverSelfSignedCertExpirationPromDuration)
	}

	// Validate CLI flags.
	if cfg.kubeNamespace == "" {
		fatal(errors.New("The Kubernetes namespace has not been specified."))
	}
	if (cfg.kubeAPIURL == "") != (cfg.kubeConfigFile == "") {
		fatal(errors.New("Either configure both Kubernetes API URL and config file or none of them."))
	}

	logger, err := initLogger(cfg.logLevel)
	if err != nil {
		fatal(err)
	}

	reg := prometheus.NewRegistry()
	ready := atomic.NewBool(false)
	restart := make(chan string)

	// Expose HTTP endpoints.
	srv := newServer(cfg.serverPort, logger)
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
	kubeConfig, err := buildKubeConfig(cfg.kubeAPIURL, cfg.kubeConfigFile)
	if err != nil {
		fatal(errors.Wrap(err, "failed to build Kubernetes config"))
	}

	kubeClient, err := kubernetes.NewForConfig(kubeConfig)
	if err != nil {
		fatal(errors.Wrap(err, "failed to build Kubernetes client"))
	}

	if cfg.serverTLSEnabled {
		var certProvider tlscert.Provider
		if cfg.serverSelfSignedCert {
			if cfg.serverSelfSignedCertDNSName == "" {
				cfg.serverSelfSignedCertDNSName = fmt.Sprintf("rollout-operator.%s.svc", cfg.kubeNamespace)
			}
			selfSignedProvider := tlscert.NewSelfSignedCertProvider("rollout-operator", []string{cfg.serverSelfSignedCertDNSName}, []string{cfg.serverSelfSignedCertOrg}, serverSelfSignedCertExpiration)
			certProvider = tlscert.NewKubeSecretPersistedCertProvider(selfSignedProvider, logger, kubeClient, cfg.kubeNamespace, cfg.serverSelfSignedCertSecretName)
		} else if cfg.serverCertFile != "" && cfg.serverKeyFile != "" {
			certProvider, err = tlscert.NewFileCertProvider(cfg.serverCertFile, cfg.serverKeyFile)
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
		checkAndWatchCertificate(cert, logger, restart)

		if cfg.updateWebhooksWithSelfSignedCABundle {
			if !cfg.serverSelfSignedCert {
				fatal(errors.New("self-signed certificate should be enabled to update the CA bundle in the webhook configurations"))
			}

			// TODO watch webhook configurations instead of doing one-off.
			err = admission.PatchCABundleOnValidatingWebhooks(context.Background(), logger, kubeClient, cfg.kubeNamespace, cert.CA)
			if err != nil {
				fatal(err)
			}
		}

		tlsSrv, err := newTLSServer(cfg.serverTLSPort, logger, cert)
		if err != nil {
			fatal(err)
		}
		tlsSrv.Handle(admission.NoDownscaleWebhookPath, admission.Serve(admission.NoDownscale, logger, kubeClient))
		if err := tlsSrv.Start(); err != nil {
			fatal(err)
		}
	}

	// Init the controller.
	c := controller.NewRolloutController(kubeClient, cfg.kubeNamespace, cfg.reconcileInterval, reg, logger)
	if err := c.Init(); err != nil {
		fatal(errors.Wrap(err, "error while initialising the controller"))
	}

	// Listen to sigterm, as well as for restart (like for certificate renewal).
	go func() {
		sigint := make(chan os.Signal, 1)
		signal.Notify(sigint, syscall.SIGINT, syscall.SIGTERM)
		select {
		case sig := <-sigint:
			level.Info(logger).Log("msg", "received signal", "signal", sig)
		case reason := <-restart:
			level.Info(logger).Log("msg", "restarting", "reason", reason)
		}
		c.Stop()
	}()

	// The operator is ready once the controller successfully initialised.
	ready.Store(true)

	// Run and block until stopped.
	c.Run()
}

func checkAndWatchCertificate(cert tlscert.Certificate, logger log.Logger, restart chan string) {
	pair, err := tls.X509KeyPair(cert.Cert, cert.Key)
	if err != nil {
		fatal(fmt.Errorf("failed to parse the provided certificate pair: %w", err))
	}

	for i, bytes := range pair.Certificate {
		c, err := x509.ParseCertificate(bytes)
		if err != nil {
			fatal(fmt.Errorf("failed to parse the provided certificate %d: %s", i, err))
		}

		expiresIn := time.Until(c.NotAfter)
		if expiresIn <= 0 {
			fatal(fmt.Errorf("the provided certificate %d for %s issued by %s is expired: notAfter=%s", i, c.Subject, c.Issuer, c.NotAfter))
		}

		level.Info(logger).Log("msg", "setting restart timer for CA certificate expiration", "expires_in", expiresIn, "expires_at", c.NotAfter, "subject", c.Subject, "issuer", c.Issuer)
		time.AfterFunc(expiresIn, func() {
			restart <- fmt.Sprintf("certificate for %s issued by %s expired", c.Subject, c.Issuer)
		})
	}

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
