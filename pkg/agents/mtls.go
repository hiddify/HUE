package agents

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

// AgentDialOption builds a grpc.DialOption that presents a client
// certificate to HUE (for mTLS). All three paths are required:
//
//   - certFile — PEM-encoded client certificate
//   - keyFile  — PEM-encoded private key matching certFile
//   - caFile   — PEM-encoded CA that signed HUE's server certificate
//
// Pass the returned option to xray.New via xray.WithDialOption.
func AgentDialOption(certFile, keyFile, caFile string) (grpc.DialOption, error) {
	if certFile == "" || keyFile == "" || caFile == "" {
		return nil, errors.New("agents/mtls: certFile, keyFile, and caFile are all required")
	}

	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("agents/mtls: load client key pair: %w", err)
	}

	caPEM, err := os.ReadFile(caFile)
	if err != nil {
		return nil, fmt.Errorf("agents/mtls: read CA file %q: %w", caFile, err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, errors.New("agents/mtls: no valid PEM certificate found in CA file")
	}

	tlsCfg := &tls.Config{
		Certificates: []tls.Certificate{cert},
		RootCAs:      pool,
		MinVersion:   tls.VersionTLS12,
	}
	return grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg)), nil
}
