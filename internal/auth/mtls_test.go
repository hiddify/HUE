package auth

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"testing"
	"time"
)

// selfSignedCA generates a minimal self-signed CA cert + key in temp
// files and returns their paths. The test registers cleanup.
func selfSignedCA(t *testing.T) (certFile string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test-ca"},
		NotBefore:             time.Now().Add(-time.Minute),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.CreateTemp(t.TempDir(), "ca-*.pem")
	if err != nil {
		t.Fatal(err)
	}
	if err := pem.Encode(f, &pem.Block{Type: "CERTIFICATE", Bytes: der}); err != nil {
		t.Fatal(err)
	}
	f.Close()
	return f.Name()
}

func TestLoadMTLSClientCA_Empty(t *testing.T) {
	pool, err := LoadMTLSClientCA("")
	if err != nil || pool != nil {
		t.Errorf("empty caFile: got pool=%v err=%v, want nil,nil", pool, err)
	}
}

func TestLoadMTLSClientCA_ValidCA(t *testing.T) {
	caFile := selfSignedCA(t)
	pool, err := LoadMTLSClientCA(caFile)
	if err != nil {
		t.Fatalf("LoadMTLSClientCA: %v", err)
	}
	if pool == nil {
		t.Fatal("expected non-nil pool")
	}
}

func TestLoadMTLSClientCA_MissingFile(t *testing.T) {
	_, err := LoadMTLSClientCA("/nonexistent/path/ca.pem")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestLoadMTLSClientCA_InvalidPEM(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "bad-*.pem")
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString("this is not a valid PEM certificate\n")
	f.Close()
	_, err = LoadMTLSClientCA(f.Name())
	if err == nil {
		t.Fatal("expected error for invalid PEM")
	}
}

func TestApplyMTLS_NilPool(t *testing.T) {
	cfg := &tls.Config{}
	ApplyMTLS(cfg, nil)
	if cfg.ClientAuth != tls.NoClientCert {
		t.Errorf("nil pool: ClientAuth = %v, want NoClientCert", cfg.ClientAuth)
	}
	if cfg.ClientCAs != nil {
		t.Errorf("nil pool: ClientCAs should remain nil")
	}
}

func TestApplyMTLS_WithPool(t *testing.T) {
	caFile := selfSignedCA(t)
	pool, err := LoadMTLSClientCA(caFile)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &tls.Config{}
	ApplyMTLS(cfg, pool)
	if cfg.ClientAuth != tls.RequireAndVerifyClientCert {
		t.Errorf("ClientAuth = %v, want RequireAndVerifyClientCert", cfg.ClientAuth)
	}
	if cfg.ClientCAs == nil {
		t.Error("ClientCAs should be set")
	}
}
