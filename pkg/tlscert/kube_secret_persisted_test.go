package tlscert

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"testing"
	"time"

	"github.com/go-kit/log"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

const (
	testNamespace  = "default"
	testSecretName = "rollout-operator-self-signed-certificate"
)

type staticCertProvider struct {
	cert  Certificate
	err   error
	calls int
}

func (p *staticCertProvider) Certificate(context.Context) (Certificate, error) {
	p.calls++
	return p.cert, p.err
}

func TestKubeSecretPersistedCertProvider_CreatesSecretWhenMissing(t *testing.T) {
	ctx := context.Background()
	client := fake.NewClientset()
	generated := mustGenerateTestCertificate(t, time.Now(), time.Now().Add(time.Hour))
	provider := &staticCertProvider{cert: generated}

	cp := NewKubeSecretPersistedCertProvider(provider, log.NewNopLogger(), client, testNamespace, testSecretName)
	got, err := cp.Certificate(ctx)
	require.NoError(t, err)
	require.Equal(t, generated, got)
	require.Equal(t, 1, provider.calls)

	secret, err := client.CoreV1().Secrets(testNamespace).Get(ctx, testSecretName, metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, generated.CA, secret.Data["ca"])
	require.Equal(t, generated.Cert, secret.Data["cert"])
	require.Equal(t, generated.Key, secret.Data["key"])
}

func TestKubeSecretPersistedCertProvider_ReusesValidSecret(t *testing.T) {
	ctx := context.Background()
	existing := mustGenerateTestCertificate(t, time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	client := fake.NewClientset(namespacedCertificateSecret(existing))
	provider := &staticCertProvider{cert: mustGenerateTestCertificate(t, time.Now(), time.Now().Add(2*time.Hour))}

	cp := NewKubeSecretPersistedCertProvider(provider, log.NewNopLogger(), client, testNamespace, testSecretName)
	got, err := cp.Certificate(ctx)
	require.NoError(t, err)
	require.Equal(t, existing, got)
	require.Equal(t, 0, provider.calls, "should not regenerate when the stored certificate is still valid")
}

func TestKubeSecretPersistedCertProvider_UpdatesEmptySecret(t *testing.T) {
	ctx := context.Background()
	client := fake.NewClientset(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: testSecretName, Namespace: testNamespace},
		Type:       corev1.SecretTypeOpaque,
	})
	generated := mustGenerateTestCertificate(t, time.Now(), time.Now().Add(time.Hour))
	provider := &staticCertProvider{cert: generated}

	cp := NewKubeSecretPersistedCertProvider(provider, log.NewNopLogger(), client, testNamespace, testSecretName)
	got, err := cp.Certificate(ctx)
	require.NoError(t, err)
	require.Equal(t, generated, got)
	require.Equal(t, 1, provider.calls)

	secret, err := client.CoreV1().Secrets(testNamespace).Get(ctx, testSecretName, metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, generated.CA, secret.Data["ca"])
	require.Equal(t, generated.Cert, secret.Data["cert"])
	require.Equal(t, generated.Key, secret.Data["key"])
}

func TestKubeSecretPersistedCertProvider_RenewsExpiredSecret(t *testing.T) {
	ctx := context.Background()
	expired := mustGenerateTestCertificate(t, time.Now().Add(-2*time.Hour), time.Now().Add(-time.Hour))
	client := fake.NewClientset(namespacedCertificateSecret(expired))
	renewed := mustGenerateTestCertificate(t, time.Now(), time.Now().Add(time.Hour))
	provider := &staticCertProvider{cert: renewed}

	cp := NewKubeSecretPersistedCertProvider(provider, log.NewNopLogger(), client, testNamespace, testSecretName)
	got, err := cp.Certificate(ctx)
	require.NoError(t, err)
	require.Equal(t, renewed, got)
	require.Equal(t, 1, provider.calls)

	secret, err := client.CoreV1().Secrets(testNamespace).Get(ctx, testSecretName, metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, renewed.CA, secret.Data["ca"])
	require.Equal(t, renewed.Cert, secret.Data["cert"])
	require.Equal(t, renewed.Key, secret.Data["key"])

	pair, err := tls.X509KeyPair(secret.Data["cert"], secret.Data["key"])
	require.NoError(t, err)
	parsed, err := x509.ParseCertificate(pair.Certificate[0])
	require.NoError(t, err)
	require.True(t, parsed.NotAfter.After(time.Now()), "renewed certificate should not be expired")
}

func TestKubeSecretPersistedCertProvider_RejectsInvalidSecret(t *testing.T) {
	ctx := context.Background()
	client := fake.NewClientset(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: testSecretName, Namespace: testNamespace},
		Data: map[string][]byte{
			"ca":   []byte("not-a-cert"),
			"cert": []byte("not-a-cert"),
			"key":  []byte("not-a-key"),
		},
	})
	provider := &staticCertProvider{cert: mustGenerateTestCertificate(t, time.Now(), time.Now().Add(time.Hour))}

	cp := NewKubeSecretPersistedCertProvider(provider, log.NewNopLogger(), client, testNamespace, testSecretName)
	_, err := cp.Certificate(ctx)
	require.Error(t, err)
	require.Equal(t, 0, provider.calls, "invalid certificate material should fail without regenerating")
}

func namespacedCertificateSecret(cert Certificate) *corev1.Secret {
	secret := certificateToSecret(testSecretName, cert)
	secret.Namespace = testNamespace
	return secret
}

func mustGenerateTestCertificate(t *testing.T, notBefore, notAfter time.Time) Certificate {
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

	return Certificate{
		CA:   encodePEM(t, "CERTIFICATE", caBytes),
		Cert: encodePEM(t, "CERTIFICATE", certBytes),
		Key:  encodePEM(t, "RSA PRIVATE KEY", x509.MarshalPKCS1PrivateKey(key)),
	}
}

func encodePEM(t *testing.T, typ string, der []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	require.NoError(t, pem.Encode(&buf, &pem.Block{Type: typ, Bytes: der}))
	return buf.Bytes()
}
