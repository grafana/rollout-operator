package tlscert

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"time"

	"github.com/go-kit/log"
	"github.com/go-kit/log/level"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// NewKubeSecretPersistedCertProvider returns a new Provider that wraps another Provider and persists the certificate in a Kubernetes secret.
func NewKubeSecretPersistedCertProvider(provider Provider, logger log.Logger, kubeClient kubernetes.Interface, namespace, secretName string) KubeSecretPersistedCertProvider {
	return KubeSecretPersistedCertProvider{
		provider:   provider,
		logger:     logger,
		kubeClient: kubeClient,
		namespace:  namespace,
		secretName: secretName,
	}
}

type KubeSecretPersistedCertProvider struct {
	provider   Provider
	logger     log.Logger
	kubeClient kubernetes.Interface
	namespace  string
	secretName string
}

func (cp KubeSecretPersistedCertProvider) Certificate(ctx context.Context) (Certificate, error) {
	secret, err := cp.kubeClient.CoreV1().Secrets(cp.namespace).Get(ctx, cp.secretName, metav1.GetOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return Certificate{}, fmt.Errorf("failed to get secret: %w", err)
	}

	found := false
	if err == nil {
		level.Debug(cp.logger).Log("msg", "found existing certificate secret", "secret", cp.secretName)
		if containsValidCert, err := cp.containsValidCertificate(secret); err != nil {
			return Certificate{}, err
		} else if containsValidCert {
			return secretToCertificate(secret), nil
		}
		found = true
	}

	cert, err := cp.provider.Certificate(ctx)
	if err != nil {
		return Certificate{}, fmt.Errorf("failed to generate ca and certificate key pair: %w", err)
	}

	if found {
		// We already had a secret, we need to update it.
		return cp.updateCertificateSecret(ctx, cert)
	}

	// There was no secret, so we need to create one from scratch.
	return cp.createCertificateSecret(ctx, cert)
}

func (cp KubeSecretPersistedCertProvider) updateCertificateSecret(ctx context.Context, cert Certificate) (Certificate, error) {
	level.Info(cp.logger).Log("msg", "updating certificate secret", "secret", cp.secretName)
	secret, err := cp.kubeClient.CoreV1().Secrets(cp.namespace).Update(ctx, certificateToSecret(cp.secretName, cert), metav1.UpdateOptions{})
	if err != nil {
		level.Error(cp.logger).Log("msg", "failed to update certificate secret", "err", err)
		return Certificate{}, fmt.Errorf("failed to update certificate secret: %w", err)
	}
	return secretToCertificate(secret), nil
}

func (cp KubeSecretPersistedCertProvider) createCertificateSecret(ctx context.Context, cert Certificate) (Certificate, error) {
	level.Info(cp.logger).Log("msg", "creating certificate secret", "secret", cp.secretName)
	secret, err := cp.kubeClient.CoreV1().Secrets(cp.namespace).Create(ctx, certificateToSecret(cp.secretName, cert), metav1.CreateOptions{})
	if err == nil {
		level.Debug(cp.logger).Log("msg", "created certificate secret", "secret", cp.secretName)
		return secretToCertificate(secret), nil
	}

	if apierrors.IsAlreadyExists(err) {
		level.Warn(cp.logger).Log("msg", "certificate secret already exists, maybe a race condition? will try to read again", "secret", cp.secretName)
		secret, err := cp.kubeClient.CoreV1().Secrets(cp.namespace).Get(ctx, cp.secretName, metav1.GetOptions{})
		if err != nil {
			return Certificate{}, fmt.Errorf("failed to get secret after being unable to create it: %w", err)
		}
		if containsValidCert, err := cp.containsValidCertificate(secret); err != nil {
			return Certificate{}, err
		} else if containsValidCert {
			return secretToCertificate(secret), nil
		}
		return Certificate{}, fmt.Errorf("tried to create a cert because it didn't exist, but it exists now and it doesn't contain a valid certificate (see previous logs for details)")
	}

	return Certificate{}, fmt.Errorf("failed to create secret: %w", err)
}

// containsValidCertificate will check whether an existing certificate secret contains a valid certificate.
func (cp KubeSecretPersistedCertProvider) containsValidCertificate(secret *corev1.Secret) (containsValidCert bool, err error) {
	if len(secret.Data) == 0 {
		level.Warn(cp.logger).Log("msg", "found a certificate secret but it's empty, will update with new certificate data", "secret", cp.secretName)
		return false, nil
	}

	if err := checkSecretDataFields(secret, cp.secretName, cp.namespace); err != nil {
		return false, err
	}

	pair, err := tls.X509KeyPair(secret.Data["cert"], secret.Data["key"])
	if err != nil {
		return false, fmt.Errorf("secret %s does not contain a valid certificate pair: %s, delete the secret and try again", cp.secretName, err)
	}

	if len(pair.Certificate) == 0 {
		return false, fmt.Errorf("secret %s does not contain a valid certificate pair: no certificates found", cp.secretName)
	}

	for i, bytes := range pair.Certificate {
		c, err := x509.ParseCertificate(bytes)
		if err != nil {
			return false, fmt.Errorf("secret %s does not contain a valid certificate %d: %s, delete the secret and try again", cp.secretName, i, err)
		}

		validUntil := c.NotAfter
		if validUntil.Before(time.Now()) {
			level.Info(cp.logger).Log("msg", "found certificate is expired", "secret", cp.secretName, "valid_until", validUntil, "subject", c.Subject, "issuer", c.Issuer, "index", i)
			return false, nil
		}

		level.Info(cp.logger).Log("msg", "found certificate is still valid", "secret", cp.secretName, "valid_until", validUntil, "subject", c.Subject, "issuer", c.Issuer, "index", i)
	}

	return true, nil
}

// secretToCertificate converts a secret to a Certificate.
// It expects always a valid secret with all the required fields, and panics otherwise.
// The check for the fields is merely an implementation-error safeguard.
func secretToCertificate(secret *corev1.Secret) Certificate {
	if err := checkSecretDataFields(secret, secret.Name, secret.Namespace); err != nil {
		panic("secretToCertificate called with invalid secret: " + err.Error())
	}

	return Certificate{
		CA:   secret.Data["ca"],
		Cert: secret.Data["cert"],
		Key:  secret.Data["key"],
	}
}

func certificateToSecret(secretName string, cert Certificate) *corev1.Secret {
	newSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name: secretName,
		},
		Data: map[string][]byte{
			"ca":   cert.CA,
			"cert": cert.Cert,
			"key":  cert.Key,
		},
	}
	return newSecret
}

func checkSecretDataFields(secret *corev1.Secret, name, namespace string) error {
	if _, ok := secret.Data["ca"]; !ok {
		return fmt.Errorf("secret %q in namespace %q does not contain a ca key, delete the secret and try again", name, namespace)
	}
	if _, ok := secret.Data["cert"]; !ok {
		return fmt.Errorf("secret %q in namespace %q does not contain a cert key, delete the secret and try again", name, namespace)
	}
	if _, ok := secret.Data["key"]; !ok {
		return fmt.Errorf("secret %q in namespace %q does not contain a key key, delete the secret and try again", name, namespace)
	}
	return nil
}
