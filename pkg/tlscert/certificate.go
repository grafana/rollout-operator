package tlscert

import "context"

// A Provider either provides or creates certificates.
type Provider interface {
	Certificate(context.Context) (Certificate, error)
}

type Certificate struct {
	// CA might be empty for non self-signed certificates.
	CA []byte

	// Cert is the certificate.
	Cert []byte
	Key  []byte
}
