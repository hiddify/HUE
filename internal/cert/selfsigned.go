// Package cert handles certificate store I/O — issuance (self-signed +
// ACME), parsing/validation of imported PEM, and AES-GCM-at-rest of
// private keys.
//
// Phase 2.5 ships self-signed + BYO; ACME (Let's Encrypt via lego/v4)
// lands in 2.5b alongside the HTTP-01 challenge endpoint plumbing.
package cert

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"time"
)

// SelfSigned generates a 30-year self-signed cert covering domain_names
// (which may include "*.x.y" wildcards and bare IPs). Returns
// public-PEM + private-key-PEM.
//
// Uses ECDSA-P-256 keys — smaller than RSA, broadly supported by
// modern TLS stacks, fast handshake.
func SelfSigned(domainNames []string) (publicPEM, privatePEM []byte, expiresAt time.Time, err error) {
	if len(domainNames) == 0 {
		return nil, nil, time.Time{}, fmt.Errorf("cert: at least one domain_name required")
	}
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, time.Time{}, fmt.Errorf("cert: generate key: %w", err)
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, nil, time.Time{}, fmt.Errorf("cert: serial: %w", err)
	}

	notBefore := time.Now().UTC()
	notAfter := notBefore.AddDate(30, 0, 0)
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: domainNames[0]},
		NotBefore:    notBefore,
		NotAfter:     notAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
	}
	for _, d := range domainNames {
		if ip := net.ParseIP(d); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
			continue
		}
		tmpl.DNSNames = append(tmpl.DNSNames, d)
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		return nil, nil, time.Time{}, fmt.Errorf("cert: create: %w", err)
	}
	publicPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})

	privDER, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		return nil, nil, time.Time{}, fmt.Errorf("cert: marshal private: %w", err)
	}
	privatePEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: privDER})

	return publicPEM, privatePEM, notAfter, nil
}

// ParseAndValidate decodes a PEM certificate and asserts:
//   * it parses cleanly,
//   * notAfter is in the future,
//   * all of `mustCover` is in the cert's DNSNames / IPAddresses
//     (wildcards expanded per RFC 6125 §6.4.3).
//
// Used by DomainCertificateService.AddCertificate to reject any
// upload that doesn't materially improve the store.
func ParseAndValidate(publicPEM []byte, mustCover []string) (*x509.Certificate, error) {
	block, _ := pem.Decode(publicPEM)
	if block == nil {
		return nil, fmt.Errorf("cert: invalid PEM")
	}
	c, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("cert: parse: %w", err)
	}
	if time.Now().UTC().After(c.NotAfter) {
		return nil, fmt.Errorf("cert: already expired (%s)", c.NotAfter)
	}
	for _, want := range mustCover {
		if !certCovers(c, want) {
			return nil, fmt.Errorf("cert: does not cover %q", want)
		}
	}
	return c, nil
}

// certCovers reports whether `host` falls under any of c's
// DNSNames (with wildcard support) or IPAddresses.
func certCovers(c *x509.Certificate, host string) bool {
	if ip := net.ParseIP(host); ip != nil {
		for _, certIP := range c.IPAddresses {
			if certIP.Equal(ip) {
				return true
			}
		}
		return false
	}
	for _, n := range c.DNSNames {
		if matchesHost(n, host) {
			return true
		}
	}
	return false
}

func matchesHost(pattern, host string) bool {
	if pattern == host {
		return true
	}
	if len(pattern) < 2 || pattern[0] != '*' || pattern[1] != '.' {
		return false
	}
	suffix := pattern[1:] // ".example.com"
	if len(host) <= len(suffix) {
		return false
	}
	if host[len(host)-len(suffix):] != suffix {
		return false
	}
	// Ensure no extra label depth — *.example.com matches a.example.com
	// but not a.b.example.com.
	prefix := host[:len(host)-len(suffix)]
	for i := 0; i < len(prefix); i++ {
		if prefix[i] == '.' {
			return false
		}
	}
	return true
}
