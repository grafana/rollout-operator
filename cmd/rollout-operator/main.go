package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"flag"
	"fmt"
	"net/http"
	_ "net/http/pprof" // anonymous import to get the pprof handler registered
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-kit/log"
	"github.com/go-kit/log/level"
	"github.com/grafana/dskit/tracing"
	"github.com/pkg/errors"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/prometheus/common/model"
	"go.uber.org/atomic"
	v1 "k8s.io/api/admission/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/grafana/rollout-operator/pkg/admission"
	"github.com/grafana/rollout-operator/pkg/controller"
	"github.com/grafana/rollout-operator/pkg/tlscert"

	// Required to get the GCP auth provider working.
	_ "k8s.io/client-go/plugin/pkg/client/auth/gcp"
)

const defaultServerSelfSignedCertExpiration = model.Duration(365 * 24 * time.Hour)

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

	serverSelfSignedCert           bool
	serverSelfSignedCertSecretName string
	serverSelfSignedCertDNSName    string
	serverSelfSignedCertOrg        string
	serverSelfSignedCertExpiration model.Duration

	updateWebhooksWithSelfSignedCABundle bool

	useZoneTracker           bool
	zoneTrackerConfigMapName string
}

func (cfg *config) register(fs *flag.FlagSet) {
	fs.StringVar(&cfg.logLevel, "log.level", "debug", "The log level. Supported values: debug, info, warn, error.")
	fs.IntVar(&cfg.serverPort, "server.port", 8001, "Port to use for exposing instrumentation and readiness probe endpoints.")
	fs.StringVar(&cfg.kubeAPIURL, "kubernetes.api-url", "", "The Kubernetes server API URL. If not specified, it will be auto-detected when running within a Kubernetes cluster.")
	fs.StringVar(&cfg.kubeConfigFile, "kubernetes.config-file", "", "The Kubernetes config file path. If not specified, it will be auto-detected when running within a Kubernetes cluster.")
	fs.StringVar(&cfg.kubeNamespace, "kubernetes.namespace", "", "The Kubernetes namespace for which this operator is running.")
	fs.DurationVar(&cfg.reconcileInterval, "reconcile.interval", 5*time.Second, "The minimum interval of reconciliation.")

	fs.BoolVar(&cfg.serverTLSEnabled, "server-tls.enabled", false, "Enable TLS server for webhook connections.")
	fs.IntVar(&cfg.serverTLSPort, "server-tls.port", 8443, "Port to use for exposing TLS server for webhook connections (if enabled).")
	fs.StringVar(&cfg.serverCertFile, "server-tls.cert-file", "", "Path to the TLS certificate file if not using the self-signed certificate.")
	fs.StringVar(&cfg.serverKeyFile, "server-tls.key-file", "", "Path to the TLS private key file if not using the self-signed certificate.")

	fs.BoolVar(&cfg.serverSelfSignedCert, "server-tls.self-signed-cert.enabled", true, "Generate a self-signed certificate for the TLS server.")
	fs.StringVar(&cfg.serverSelfSignedCertSecretName, "server-tls.self-signed-cert.secret-name", "rollout-operator-self-signed-certificate", "Secret name to store the self-signed certificate (if enabled).")
	fs.StringVar(&cfg.serverSelfSignedCertDNSName, "server-tls.self-signed-cert.dns-name", "", "DNS name to use for the self-signed certificate (if enabled). If left empty, then 'rollout-operator.<namespace>.svc' will be used.")
	fs.StringVar(&cfg.serverSelfSignedCertOrg, "server-tls.self-signed-cert.org", "Grafana Labs", "Organization name to use for the self-signed certificate (if enabled).")
	fs.Var(&cfg.serverSelfSignedCertExpiration, "server-tls.self-signed-cert.expiration", "Expiration time for the self-signed certificate in Prometheus duration format (Go format plus support for days, weeks and years as 1d/1w/1y).")
	cfg.serverSelfSignedCertExpiration = defaultServerSelfSignedCertExpiration

	fs.BoolVar(&cfg.updateWebhooksWithSelfSignedCABundle, "webhooks.update-ca-bundle", true, "Update the CA bundle in the properly labeled webhook configurations with the self-signed certificate (-server-tls.self-signed-cert.enabled should be enabled).")

	fs.BoolVar(&cfg.useZoneTracker, "use-zone-tracker", false, "Use the zone tracker to prevent simultaneous downscales in different zones")
	fs.StringVar(&cfg.zoneTrackerConfigMapName, "zone-tracker.config-map-name", "rollout-operator-zone-tracker", "The name of the ConfigMap to use for the zone tracker")
}

func (cfg config) validate() error {
	// Validate CLI flags.
	if cfg.kubeNamespace == "" {
		return errors.New("the Kubernetes namespace has not been specified")
	}
	if (cfg.kubeAPIURL == "") != (cfg.kubeConfigFile == "") {
		return errors.New("either configure both Kubernetes API URL and config file or none of them")
	}
	if cfg.useZoneTracker && cfg.zoneTrackerConfigMapName == "" {
		return errors.New("the zone tracker ConfigMap name has not been specified")
	}

	return nil
}

func main() {
	// CLI flags.
	var cfg config
	fs := flag.NewFlagSet("rollout-operator", flag.ExitOnError)
	cfg.register(fs)
	check(fs.Parse(os.Args[1:]))
	check(cfg.validate())

	logger, err := initLogger(cfg.logLevel)
	check(err)

	reg := prometheus.NewRegistry()
	metrics := newMetrics(reg)
	ready := atomic.NewBool(false)
	restart := make(chan string)

	name := os.Getenv("JAEGER_SERVICE_NAME")
	if name == "" {
		name = "rollout-operator"
	}

	if trace, err := tracing.NewFromEnv(name); err != nil {
		level.Error(logger).Log("msg", "Failed to setup tracing", "err", err.Error())
	} else {
		defer trace.Close()
	}

	// Expose HTTP endpoints.
	srv := newServer(cfg.serverPort, logger, metrics)
	srv.Handle("/metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))
	srv.Handle("/ready", readyHandler(ready))
	srv.PathPrefix("/debug/pprof").Handler(http.DefaultServeMux)
	check(srv.Start())

	// Build the Kubernetes client config.
	kubeConfig, err := buildKubeConfig(cfg.kubeAPIURL, cfg.kubeConfigFile)
	check(errors.Wrap(err, "failed to build Kubernetes client config"))

	kubeClient, err := kubernetes.NewForConfig(kubeConfig)
	check(errors.Wrap(err, "failed to create Kubernetes client"))

	// Start TLS server if enabled.
	maybeStartTLSServer(cfg, logger, kubeClient, restart, metrics)

	// Init the controller.
	c := controller.NewRolloutController(kubeClient, cfg.kubeNamespace, cfg.reconcileInterval, reg, logger)
	check(errors.Wrap(c.Init(), "failed to init controller"))

	// Listen to sigterm, as well as for restart (like for certificate renewal).
	go func() {
		waitForSignalOrRestart(logger, restart)
		c.Stop()
	}()

	// The operator is ready once the controller successfully initialised.
	ready.Store(true)

	// Run and block until stopped.
	c.Run()
}

func waitForSignalOrRestart(logger log.Logger, restart chan string) {
	sigint := make(chan os.Signal, 1)
	signal.Notify(sigint, syscall.SIGINT, syscall.SIGTERM)
	select {
	case sig := <-sigint:
		level.Info(logger).Log("msg", "received signal", "signal", sig)
	case reason := <-restart:
		level.Info(logger).Log("msg", "restarting", "reason", reason)
	}
}

func maybeStartTLSServer(cfg config, logger log.Logger, kubeClient *kubernetes.Clientset, restart chan string, metrics *metrics) {
	if !cfg.serverTLSEnabled {
		level.Info(logger).Log("msg", "tls server is not enabled")
		return
	}

	var certProvider tlscert.Provider
	var err error
	if cfg.serverSelfSignedCert {
		if cfg.serverSelfSignedCertDNSName == "" {
			cfg.serverSelfSignedCertDNSName = fmt.Sprintf("rollout-operator.%s.svc", cfg.kubeNamespace)
		}
		selfSignedProvider := tlscert.NewSelfSignedCertProvider("rollout-operator", []string{cfg.serverSelfSignedCertDNSName}, []string{cfg.serverSelfSignedCertOrg}, time.Duration(cfg.serverSelfSignedCertExpiration))
		certProvider = tlscert.NewKubeSecretPersistedCertProvider(selfSignedProvider, logger, kubeClient, cfg.kubeNamespace, cfg.serverSelfSignedCertSecretName)
	} else if cfg.serverCertFile != "" && cfg.serverKeyFile != "" {
		certProvider, err = tlscert.NewFileCertProvider(cfg.serverCertFile, cfg.serverKeyFile)
		check(errors.Wrap(err, "failed to create file cert provider"))
	} else {
		fatal(errors.New("either self-signed certificate should be enabled or path to the certificate and key should be provided"))
	}

	cert, err := certProvider.Certificate(context.Background())
	check(errors.Wrap(err, "failed to get certificate"))

	checkAndWatchCertificate(cert, logger, restart)

	if cfg.updateWebhooksWithSelfSignedCABundle {
		if !cfg.serverSelfSignedCert {
			fatal(errors.New("self-signed certificate should be enabled to update the CA bundle in the webhook configurations"))
		}

		// TODO watch webhook configurations instead of doing one-off.
		check(tlscert.PatchCABundleOnValidatingWebhooks(context.Background(), logger, kubeClient, cfg.kubeNamespace, cert.CA))
		check(tlscert.PatchCABundleOnMutatingWebhooks(context.Background(), logger, kubeClient, cfg.kubeNamespace, cert.CA))
	}

	prepDownscaleAdmitFunc := func(ctx context.Context, logger log.Logger, ar v1.AdmissionReview, api *kubernetes.Clientset) *v1.AdmissionResponse {
		return admission.PrepareDownscale(ctx, logger, ar, api, cfg.useZoneTracker, cfg.zoneTrackerConfigMapName)
	}

	tlsSrv, err := newTLSServer(cfg.serverTLSPort, logger, cert, metrics)
	check(errors.Wrap(err, "failed to create tls server"))
	tlsSrv.Handle(admission.NoDownscaleWebhookPath, admission.Serve(admission.NoDownscale, logger, kubeClient))
	tlsSrv.Handle(admission.PrepareDownscaleWebhookPath, admission.Serve(prepDownscaleAdmitFunc, logger, kubeClient))
	check(errors.Wrap(tlsSrv.Start(), "failed to start tls server"))
}

func checkAndWatchCertificate(cert tlscert.Certificate, logger log.Logger, restart chan string) {
	pair, err := tls.X509KeyPair(cert.Cert, cert.Key)
	check(errors.Wrap(err, "failed to parse the provided certificate"))

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

func readyHandler(ready *atomic.Bool) http.Handler {
	return http.HandlerFunc(func(res http.ResponseWriter, _ *http.Request) {
		if ready.Load() {
			res.WriteHeader(http.StatusOK)
		} else {
			res.WriteHeader(http.StatusInternalServerError)
		}
	})
}

func check(err error) {
	if err != nil {
		fatal(err)
	}
}

func fatal(err error) {
	fmt.Fprintf(os.Stderr, "%s\n", err.Error())
	os.Exit(1)
}
