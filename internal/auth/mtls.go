package auth

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
)

// LoadMTLSClientCA reads a PEM-encoded CA certificate file and returns
// a certificate pool suitable for tls.Config.ClientCAs. When caFile is
// empty it returns nil, nil (mTLS disabled).
func LoadMTLSClientCA(caFile string) (*x509.CertPool, error) {
	if caFile == "" {
		return nil, nil
	}
	pem, err := os.ReadFile(caFile)
	if err != nil {
		return nil, fmt.Errorf("mtls: read CA file %q: %w", caFile, err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, errors.New("mtls: no valid PEM certificate found in CA file")
	}
	return pool, nil
}

// ApplyMTLS mutates cfg to require and verify client certificates using
// the given pool. Calling with a nil pool is a no-op.
//
// Must be called after the server certificate is already loaded so
// MinVersion and Certificates are already in place.
func ApplyMTLS(cfg *tls.Config, clientCAs *x509.CertPool) {
	if clientCAs == nil {
		return
	}
	cfg.ClientCAs = clientCAs
	cfg.ClientAuth = tls.RequireAndVerifyClientCert
}
