package tlscert

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"time"
)

// NewSelfSignedCertProvider creates a new certificate provider that creates a self-signed certificate.
func NewSelfSignedCertProvider(commonName string, dnsNames []string, orgs []string, expiration time.Duration) SelfSignedProvider {
	return SelfSignedProvider{commonName: commonName, dnsNames: dnsNames, orgs: orgs, expiration: expiration}
}

type SelfSignedProvider struct {
	orgs       []string
	dnsNames   []string
	commonName string
	expiration time.Duration
}

func (p SelfSignedProvider) Certificate(context.Context) (Certificate, error) {
	// init CA config
	ca := &x509.Certificate{
		SerialNumber:          big.NewInt(2022),
		Subject:               pkix.Name{Organization: p.orgs},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().Add(p.expiration),
		IsCA:                  true,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth},
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
	}

	// generate private key for CA
	caPrivateKey, err := rsa.GenerateKey(rand.Reader, 4096)
	if err != nil {
		return Certificate{}, err
	}

	// create the CA certificate
	caBytes, err := x509.CreateCertificate(rand.Reader, ca, ca, &caPrivateKey.PublicKey, caPrivateKey)
	if err != nil {
		return Certificate{}, err
	}

	// new certificate config
	newCert := &x509.Certificate{
		DNSNames:     p.dnsNames,
		SerialNumber: big.NewInt(1024),
		Subject: pkix.Name{
			CommonName:   p.commonName,
			Organization: p.orgs,
		},
		NotBefore:   time.Now(),
		NotAfter:    time.Now().Add(p.expiration),
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth},
		KeyUsage:    x509.KeyUsageDigitalSignature,
	}

	// generate new private key
	newPrivateKey, err := rsa.GenerateKey(rand.Reader, 4096)
	if err != nil {
		return Certificate{}, err
	}

	// sign the new certificate
	newCertBytes, err := x509.CreateCertificate(rand.Reader, newCert, ca, &newPrivateKey.PublicKey, caPrivateKey)
	if err != nil {
		return Certificate{}, err
	}

	return Certificate{
		CA:   pemBlockBytes(&pem.Block{Type: "CERTIFICATE", Bytes: caBytes}),
		Cert: pemBlockBytes(&pem.Block{Type: "CERTIFICATE", Bytes: newCertBytes}),
		Key:  pemBlockBytes(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(newPrivateKey)}),
	}, nil
}

func pemBlockBytes(block *pem.Block) []byte {
	buf := new(bytes.Buffer)
	if err := pem.Encode(buf, block); err != nil {
		panic(fmt.Errorf("failed writing PEM block %+v into bytes.Buffer: %s", block, err))
	}
	return buf.Bytes()
}
