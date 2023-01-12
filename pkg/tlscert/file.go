package tlscert

import (
	"context"
	"os"
)

// NewFileCertProvider creates a new certificate provider that reads the certificate and key from the given files.
func NewFileCertProvider(certFile, keyFile string) (FileCertProvider, error) {
	cert, err := os.ReadFile(certFile)
	if err != nil {
		return FileCertProvider{}, err
	}
	key, err := os.ReadFile(keyFile)
	if err != nil {
		return FileCertProvider{}, err
	}
	return FileCertProvider{
		Cert: cert,
		Key:  key,
	}, nil
}

type FileCertProvider struct {
	Cert []byte
	Key  []byte
}

func (cp FileCertProvider) Certificate(context.Context) (Certificate, error) {
	return Certificate{
		Cert: cp.Cert,
		Key:  cp.Key,
	}, nil
}
