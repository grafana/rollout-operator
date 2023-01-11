package tlscert

import (
	"context"
	"fmt"

	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
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
	secret, err := cp.getOrCreateCertificateSecret(ctx, cp.namespace, cp.secretName)
	if err != nil {
		return Certificate{}, err
	}

	if _, ok := secret.Data["ca"]; !ok {
		return Certificate{}, fmt.Errorf("secret %q in namespace %q does not contain a ca key, delete the secret and try again", cp.secretName, cp.namespace)
	}
	if _, ok := secret.Data["cert"]; !ok {
		return Certificate{}, fmt.Errorf("secret %q in namespace %q does not contain a cert key, delete the secret and try again", cp.secretName, cp.namespace)
	}
	if _, ok := secret.Data["key"]; !ok {
		return Certificate{}, fmt.Errorf("secret %q in namespace %q does not contain a key key, delete the secret and try again", cp.secretName, cp.namespace)
	}

	return Certificate{
		CA:   secret.Data["ca"],
		Cert: secret.Data["cert"],
		Key:  secret.Data["key"],
	}, nil
}

func (cp KubeSecretPersistedCertProvider) getOrCreateCertificateSecret(ctx context.Context, namespace, secretName string) (*corev1.Secret, error) {
	found := false
	secret, err := cp.kubeClient.CoreV1().Secrets(namespace).Get(ctx, secretName, metav1.GetOptions{})
	if err == nil {
		level.Debug(cp.logger).Log("msg", "found existing certificate secret", "secret", secretName)
		if len(secret.Data) > 0 {
			return secret, nil
		}
		level.Warn(cp.logger).Log("msg", "found a certificate secret but it's empty, will update with new certificate data", "secret", secretName)
		found = true
	} else if !apierrors.IsNotFound(err) {
		return nil, fmt.Errorf("failed to get secret: %w", err)
	}

	level.Info(cp.logger).Log("msg", "creating certificate secret", "secret", secretName)
	cert, err := cp.provider.Certificate(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to generate ca and certificate key pair: %w", err)
	}

	if found {
		// We already had a secret, we need to update it.
		secret, err := cp.kubeClient.CoreV1().Secrets(namespace).Update(ctx, newCertSecret(secretName, cert), metav1.UpdateOptions{})
		if err != nil {
			level.Error(cp.logger).Log("msg", "failed to update certificate secret", "err", err)
			return nil, fmt.Errorf("failed to update certificate secret: %w", err)
		}
		return secret, nil
	}

	// There was no secret, so we need to create one from scratch.
	secret, err = cp.kubeClient.CoreV1().Secrets(namespace).Create(ctx, newCertSecret(secretName, cert), metav1.CreateOptions{})
	if err == nil {
		level.Debug(cp.logger).Log("msg", "created certificate secret", "secret", secretName)
		return secret, nil
	}

	if apierrors.IsAlreadyExists(err) {
		level.Warn(cp.logger).Log("msg", "certificate secret already exists, maybe a race condition? will try to read again", "secret", secretName)
		secret, err := cp.kubeClient.CoreV1().Secrets(namespace).Get(ctx, secretName, metav1.GetOptions{})
		if err != nil {
			return nil, fmt.Errorf("failed to get secret after being unable to create it: %w", err)
		}
		return secret, nil
	}

	return nil, fmt.Errorf("failed to create secret: %w", err)
}

func newCertSecret(secretName string, cert Certificate) *corev1.Secret {
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
