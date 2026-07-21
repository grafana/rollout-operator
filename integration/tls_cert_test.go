//go:build requires_docker

package integration

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

const certificateSecretName = "rollout-operator-self-signed-certificate"

var errEmptyCertificate = errors.New("certificate secret contains no certificates")

// TestSelfSignedCertificate_EmptySecret verifies that an empty certificate secret is
// populated on startup and the no-downscale webhook works afterwards.
func TestSelfSignedCertificate_EmptySecret(t *testing.T) {
	ctx := context.Background()
	cluster := createKindCluster(t, "rollout-operator:latest", "mock-service:latest")
	api := cluster.API()
	path := initManifestFiles(t, "webhooks-enabled")

	t.Log("Create the webhook before the rollout-operator so it can inject the CA bundle.")
	webhookName := createValidatingWebhookConfiguration(t, api, ctx, path+yamlWebhookNoDownscale).Name

	t.Log("Create an empty certificate secret before the rollout-operator.")
	_, err := api.CoreV1().Secrets(corev1.NamespaceDefault).Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: certificateSecretName},
		Type:       corev1.SecretTypeOpaque,
	}, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Log("Create rollout-operator and wait until it is ready.")
	createRolloutOperator(t, ctx, api, cluster.ExtAPI(), path, true)
	requireRolloutOperatorReady(t, ctx, api)

	t.Log("Secret should now contain a valid certificate and the webhook CA should be patched.")
	requireEventuallyValidCertificateSecret(t, ctx, api)
	requireEventuallyWebhookHasCABundle(t, ctx, api, webhookName)

	requireNoDownscaleWebhookWorks(t, ctx, api)
}

// TestSelfSignedCertificate_RenewsExpiredSecretOnStartup plants an already-expired
// certificate in the secret before startup. The operator should replace it without
// waiting for a live expiry timer, then serve a working webhook.
func TestSelfSignedCertificate_RenewsExpiredSecretOnStartup(t *testing.T) {
	ctx := context.Background()
	cluster := createKindCluster(t, "rollout-operator:latest", "mock-service:latest")
	api := cluster.API()
	path := initManifestFiles(t, "webhooks-enabled")

	t.Log("Create the webhook before the rollout-operator so it can inject the CA bundle.")
	webhookName := createValidatingWebhookConfiguration(t, api, ctx, path+yamlWebhookNoDownscale).Name

	expired := mustGenerateCertificatePEM(t, time.Now().Add(-2*time.Hour), time.Now().Add(-time.Hour))
	_, err := api.CoreV1().Secrets(corev1.NamespaceDefault).Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: certificateSecretName},
		Type:       corev1.SecretTypeOpaque,
		Data: map[string][]byte{
			"ca":   expired.ca,
			"cert": expired.cert,
			"key":  expired.key,
		},
	}, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Log("Create rollout-operator and wait until it is ready.")
	createRolloutOperator(t, ctx, api, cluster.ExtAPI(), path, true)
	requireRolloutOperatorReady(t, ctx, api)

	t.Log("Expired secret material should be replaced with a still-valid certificate.")
	require.Eventually(t, func() bool {
		secret, err := api.CoreV1().Secrets(corev1.NamespaceDefault).Get(ctx, certificateSecretName, metav1.GetOptions{})
		if err != nil {
			t.Logf("secret not ready: %v", err)
			return false
		}
		if bytes.Equal(secret.Data["cert"], expired.cert) {
			t.Log("certificate has not been renewed yet")
			return false
		}
		notAfter, err := certificateNotAfter(secret)
		if err != nil {
			t.Logf("invalid renewed certificate: %v", err)
			return false
		}
		return notAfter.After(time.Now())
	}, 2*time.Minute, time.Second, "expired certificate should be renewed on startup")

	requireEventuallyWebhookHasCABundle(t, ctx, api, webhookName)
	requireNoDownscaleWebhookWorks(t, ctx, api)
}

// TestSelfSignedCertificate_RenewsAfterExpiration starts the operator with a short-lived
// certificate and waits for the expiration-driven restart to renew the secret.
// Unlike the old flaky test, this does not update the operator Deployment during the
// expired window (that race was the main source of flakes).
func TestSelfSignedCertificate_RenewsAfterExpiration(t *testing.T) {
	ctx := context.Background()
	cluster := createKindCluster(t, "rollout-operator:latest", "mock-service:latest")
	api := cluster.API()
	path := initManifestFiles(t, "webhooks-enabled")

	t.Log("Create the webhook before the rollout-operator so it can inject the CA bundle.")
	webhookName := createValidatingWebhookConfiguration(t, api, ctx, path+yamlWebhookNoDownscale).Name

	t.Log("Create rollout-operator with a short-lived self-signed certificate.")
	createRolloutOperatorDependencies(t, ctx, api, cluster.ExtAPI(), path, true)
	createRolloutOperatorDeployment(t, ctx, api, path, func(deployment *appsv1.Deployment) {
		deployment.Spec.Template.Spec.Containers[0].Args = append(
			deployment.Spec.Template.Spec.Containers[0].Args,
			// Long enough for startup and post-renewal webhook checks; short enough to keep the test bounded.
			"-server-tls.self-signed-cert.expiration=45s",
		)
	})
	requireRolloutOperatorReady(t, ctx, api)

	initialNotAfter := requireEventuallyValidCertificateSecret(t, ctx, api)
	t.Logf("Initial certificate expires at %s", initialNotAfter)

	t.Log("Wait for the operator to renew the certificate after expiration/restart.")
	var renewedNotAfter time.Time
	require.Eventually(t, func() bool {
		secret, err := api.CoreV1().Secrets(corev1.NamespaceDefault).Get(ctx, certificateSecretName, metav1.GetOptions{})
		if err != nil {
			t.Logf("secret not ready: %v", err)
			return false
		}
		notAfter, err := certificateNotAfter(secret)
		if err != nil {
			t.Logf("invalid certificate: %v", err)
			return false
		}
		if !notAfter.After(initialNotAfter) {
			t.Logf("certificate still expires at %s", notAfter)
			return false
		}
		renewedNotAfter = notAfter
		return true
	}, 3*time.Minute, time.Second, "certificate should be renewed after expiration")
	t.Logf("Renewed certificate expires at %s", renewedNotAfter)

	requireRolloutOperatorReady(t, ctx, api)
	requireEventuallyWebhookHasCABundle(t, ctx, api, webhookName)
	requireNoDownscaleWebhookWorks(t, ctx, api)
}

func createRolloutOperatorDeployment(t *testing.T, ctx context.Context, api *kubernetes.Clientset, directory string, mutate func(*appsv1.Deployment)) {
	t.Helper()

	deployment := loadFromDisk[appsv1.Deployment](t, directory+yamlDeployment, &appsv1.Deployment{})
	deployment.Spec.Template.Spec.Containers[0].Image = "rollout-operator:latest"
	deployment.Spec.Template.Spec.Containers[0].ImagePullPolicy = corev1.PullNever
	if mutate != nil {
		mutate(deployment)
	}

	_, err := api.AppsV1().Deployments(corev1.NamespaceDefault).Create(ctx, deployment, metav1.CreateOptions{})
	require.NoError(t, err)
}

func requireRolloutOperatorReady(t *testing.T, ctx context.Context, api *kubernetes.Clientset) {
	t.Helper()
	pod := eventuallyGetFirstPod(ctx, t, api, "name=rollout-operator")
	requireEventuallyPod(t, api, ctx, pod, expectPodPhase(corev1.PodRunning), expectReady())
}

func requireEventuallyValidCertificateSecret(t *testing.T, ctx context.Context, api *kubernetes.Clientset) time.Time {
	t.Helper()

	var notAfter time.Time
	require.Eventually(t, func() bool {
		secret, err := api.CoreV1().Secrets(corev1.NamespaceDefault).Get(ctx, certificateSecretName, metav1.GetOptions{})
		if err != nil {
			t.Logf("secret not ready: %v", err)
			return false
		}
		parsed, err := certificateNotAfter(secret)
		if err != nil {
			t.Logf("invalid certificate: %v", err)
			return false
		}
		if !parsed.After(time.Now()) {
			t.Logf("certificate already expired at %s", parsed)
			return false
		}
		notAfter = parsed
		return true
	}, 2*time.Minute, time.Second, "certificate secret should contain a valid certificate")
	return notAfter
}

func requireEventuallyWebhookHasCABundle(t *testing.T, ctx context.Context, api *kubernetes.Clientset, webhookName string) {
	t.Helper()

	require.Eventually(t, func() bool {
		webhook, err := api.AdmissionregistrationV1().ValidatingWebhookConfigurations().Get(ctx, webhookName, metav1.GetOptions{})
		if err != nil {
			t.Logf("webhook not ready: %v", err)
			return false
		}
		return webhookHasNonEmptyCABundle(webhook)
	}, 2*time.Minute, time.Second, "webhook CA bundle should be patched")
}

func webhookHasNonEmptyCABundle(webhook *admissionregistrationv1.ValidatingWebhookConfiguration) bool {
	for _, wh := range webhook.Webhooks {
		if len(wh.ClientConfig.CABundle) > 0 {
			return true
		}
	}
	return false
}

func requireNoDownscaleWebhookWorks(t *testing.T, ctx context.Context, api *kubernetes.Clientset) {
	t.Helper()

	mock := mockServiceStatefulSet("mock", "1", true, 1)
	mock.ObjectMeta.Labels["grafana.com/no-downscale"] = "true"

	t.Log("Create the service with one replica.")
	requireCreateStatefulSet(ctx, t, api, mock)
	requireEventuallyPodCount(ctx, t, api, "name=mock", 1)

	t.Log("Upscale should succeed with a valid webhook certificate.")
	mock.Spec.Replicas = ptr[int32](2)
	requireUpdateStatefulSet(ctx, t, api, mock)
	requireEventuallyPodCount(ctx, t, api, "name=mock", 2)
}

func certificateNotAfter(secret *corev1.Secret) (time.Time, error) {
	pair, err := tls.X509KeyPair(secret.Data["cert"], secret.Data["key"])
	if err != nil {
		return time.Time{}, err
	}
	if len(pair.Certificate) == 0 {
		return time.Time{}, errEmptyCertificate
	}
	parsed, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return time.Time{}, err
	}
	return parsed.NotAfter, nil
}

type certificatePEM struct {
	ca, cert, key []byte
}

func mustGenerateCertificatePEM(t *testing.T, notBefore, notAfter time.Time) certificatePEM {
	t.Helper()

	caKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	caTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{Organization: []string{"test"}},
		NotBefore:             notBefore,
		NotAfter:              notAfter,
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
	}
	caBytes, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	require.NoError(t, err)

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	template := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "rollout-operator", Organization: []string{"test"}},
		DNSNames:     []string{"rollout-operator.default.svc"},
		NotBefore:    notBefore,
		NotAfter:     notAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
	}
	certBytes, err := x509.CreateCertificate(rand.Reader, template, caTemplate, &key.PublicKey, caKey)
	require.NoError(t, err)

	return certificatePEM{
		ca:   encodeIntegrationPEM(t, "CERTIFICATE", caBytes),
		cert: encodeIntegrationPEM(t, "CERTIFICATE", certBytes),
		key:  encodeIntegrationPEM(t, "RSA PRIVATE KEY", x509.MarshalPKCS1PrivateKey(key)),
	}
}

func encodeIntegrationPEM(t *testing.T, typ string, der []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	require.NoError(t, pem.Encode(&buf, &pem.Block{Type: typ, Bytes: der}))
	return buf.Bytes()
}
