package main

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"testing"
	"time"

	"github.com/go-kit/log"
	"github.com/stretchr/testify/require"

	"github.com/grafana/rollout-operator/pkg/tlscert"
)

func TestWatchCertificateExpiration_SchedulesRestart(t *testing.T) {
	notAfter := time.Date(2026, 7, 20, 12, 0, 10, 0, time.UTC)
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	cert := mustTestCertificate(t, now.Add(-time.Hour), notAfter)

	restart := make(chan string, 1)
	var scheduledFor time.Duration
	var scheduledFn func()

	err := watchCertificateExpiration(cert, log.NewNopLogger(), restart, func() time.Time { return now }, func(d time.Duration, f func()) *time.Timer {
		scheduledFor = d
		scheduledFn = f
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, 10*time.Second, scheduledFor)
	require.NotNil(t, scheduledFn)

	scheduledFn()
	select {
	case reason := <-restart:
		require.Contains(t, reason, "CN=rollout-operator")
		require.Contains(t, reason, "expired")
	case <-time.After(time.Second):
		t.Fatal("expected restart signal when certificate expires")
	}
}

func TestWatchCertificateExpiration_RejectsExpiredCertificate(t *testing.T) {
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	cert := mustTestCertificate(t, now.Add(-2*time.Hour), now.Add(-time.Hour))

	err := watchCertificateExpiration(cert, log.NewNopLogger(), make(chan string, 1), func() time.Time { return now }, func(time.Duration, func()) *time.Timer {
		t.Fatal("should not schedule a restart for an already expired certificate")
		return nil
	})
	require.ErrorContains(t, err, "is expired")
}

func TestWatchCertificateExpiration_RejectsInvalidCertificate(t *testing.T) {
	err := watchCertificateExpiration(tlscert.Certificate{
		Cert: []byte("not-a-cert"),
		Key:  []byte("not-a-key"),
	}, log.NewNopLogger(), make(chan string, 1), time.Now, time.AfterFunc)
	require.ErrorContains(t, err, "failed to parse the provided certificate")
}

func mustTestCertificate(t *testing.T, notBefore, notAfter time.Time) tlscert.Certificate {
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
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	caBytes, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	require.NoError(t, err)

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	template := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "rollout-operator", Organization: []string{"test"}},
		NotBefore:    notBefore,
		NotAfter:     notAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	certBytes, err := x509.CreateCertificate(rand.Reader, template, caTemplate, &key.PublicKey, caKey)
	require.NoError(t, err)

	return tlscert.Certificate{
		CA:   encodeTestPEM(t, "CERTIFICATE", caBytes),
		Cert: encodeTestPEM(t, "CERTIFICATE", certBytes),
		Key:  encodeTestPEM(t, "RSA PRIVATE KEY", x509.MarshalPKCS1PrivateKey(key)),
	}
}

func encodeTestPEM(t *testing.T, typ string, der []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	require.NoError(t, pem.Encode(&buf, &pem.Block{Type: typ, Bytes: der}))
	return buf.Bytes()
}
