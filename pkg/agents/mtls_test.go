package agents_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"testing"
	"time"

	"github.com/hiddify/hue/pkg/agents"
)

// writePEM writes blocks to a temp file and returns the path.
func writePEM(t *testing.T, blocks ...*pem.Block) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "*.pem")
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range blocks {
		if err := pem.Encode(f, b); err != nil {
			t.Fatal(err)
		}
	}
	f.Close()
	return f.Name()
}

// makeCert generates a self-signed leaf cert signed by the given CA
// (or self-signed if ca==nil). Returns cert DER + key.
func makeClientCert(t *testing.T, caKey *ecdsa.PrivateKey, caCert *x509.Certificate) (certFile, keyFile string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "test-agent"},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
	}
	if caCert == nil {
		caCert = tmpl
		caKey = key
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, caCert, &key.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	cf := writePEM(t, &pem.Block{Type: "CERTIFICATE", Bytes: der})
	kf := writePEM(t, &pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	return cf, kf
}

func makeCA(t *testing.T) (*ecdsa.PrivateKey, *x509.Certificate, string) {
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
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	caFile := writePEM(t, &pem.Block{Type: "CERTIFICATE", Bytes: der})
	return key, cert, caFile
}

func TestAgentDialOption_Success(t *testing.T) {
	caKey, caCert, caFile := makeCA(t)
	certFile, keyFile := makeClientCert(t, caKey, caCert)
	opt, err := agents.AgentDialOption(certFile, keyFile, caFile)
	if err != nil {
		t.Fatalf("AgentDialOption: %v", err)
	}
	if opt == nil {
		t.Fatal("expected non-nil DialOption")
	}
}

func TestAgentDialOption_MissingArgs(t *testing.T) {
	cases := []struct{ cert, key, ca string }{
		{"", "k", "ca"},
		{"c", "", "ca"},
		{"c", "k", ""},
	}
	for _, tc := range cases {
		_, err := agents.AgentDialOption(tc.cert, tc.key, tc.ca)
		if err == nil {
			t.Errorf("cert=%q key=%q ca=%q: expected error", tc.cert, tc.key, tc.ca)
		}
	}
}

func TestAgentDialOption_BadCertFile(t *testing.T) {
	_, caKey, caCert, caFile := func() (string, *ecdsa.PrivateKey, *x509.Certificate, string) {
		k, c, f := makeCA(t)
		return f, k, c, f
	}()
	_ = caKey
	_ = caCert
	_, err := agents.AgentDialOption("/nonexistent/cert.pem", "/nonexistent/key.pem", caFile)
	if err == nil {
		t.Fatal("expected error for nonexistent cert/key files")
	}
}
