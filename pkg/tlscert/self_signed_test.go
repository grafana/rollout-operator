package tlscert

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSelfSignedProvider_Certificate(t *testing.T) {
	provider := NewSelfSignedCertProvider("rollout-operator", []string{"rollout-operator.default.svc"}, []string{"Grafana Labs"}, time.Hour)

	cert, err := provider.Certificate(context.Background())
	require.NoError(t, err)
	require.NotEmpty(t, cert.CA)
	require.NotEmpty(t, cert.Cert)
	require.NotEmpty(t, cert.Key)

	pair, err := tls.X509KeyPair(cert.Cert, cert.Key)
	require.NoError(t, err)
	require.Len(t, pair.Certificate, 1)

	parsed, err := x509.ParseCertificate(pair.Certificate[0])
	require.NoError(t, err)
	require.Equal(t, "rollout-operator", parsed.Subject.CommonName)
	require.Equal(t, []string{"Grafana Labs"}, parsed.Subject.Organization)
	require.Equal(t, []string{"rollout-operator.default.svc"}, parsed.DNSNames)
	require.True(t, parsed.NotAfter.After(time.Now().Add(50*time.Minute)))
	require.True(t, parsed.NotAfter.Before(time.Now().Add(70*time.Minute)))

	caPool := x509.NewCertPool()
	require.True(t, caPool.AppendCertsFromPEM(cert.CA))
	_, err = parsed.Verify(x509.VerifyOptions{Roots: caPool, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}})
	require.NoError(t, err)
}
