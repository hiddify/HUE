package cert

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"time"

	"github.com/go-acme/lego/v4/certcrypto"
	"github.com/go-acme/lego/v4/certificate"
	"github.com/go-acme/lego/v4/lego"
	"github.com/go-acme/lego/v4/registration"
)

// ACMEResult is what RequestACME returns. PrivatePEM lives in memory
// only until the caller hands it to DomainCertificate storage (which
// encrypts with AES-GCM via internal/auth.Encrypt).
type ACMEResult struct {
	DomainNames []string
	PublicPEM   []byte
	PrivatePEM  []byte
	ExpiresAt   time.Time
}

// ACMEConfig is per-request ACME settings.
type ACMEConfig struct {
	// DirectoryURL is the ACME server endpoint. Empty = production
	// Let's Encrypt. Use lego.LEDirectoryStaging for tests.
	DirectoryURL string
	// ContactEmail goes into the ACME account registration.
	ContactEmail string
	// Challenger is the shared HTTP-01 provider whose Handler the
	// HUE listener exposes.
	Challenger *HTTP01Challenger
}

// acmeUser implements lego/v4 registration.User.
type acmeUser struct {
	email        string
	registration *registration.Resource
	privateKey   crypto.PrivateKey
}

func (u *acmeUser) GetEmail() string                        { return u.email }
func (u *acmeUser) GetRegistration() *registration.Resource { return u.registration }
func (u *acmeUser) GetPrivateKey() crypto.PrivateKey        { return u.privateKey }

// RequestACME drives a single Let's Encrypt issuance for `domains`
// over HTTP-01 using the supplied challenger. Returns the resulting
// PEM material on success.
//
// Generates a fresh ACME account key per call. Production deployments
// should cache the account; phase 2.5b reissues each time for
// simplicity. Renewal is the caller's responsibility — RequestACME
// always obtains a brand-new cert.
func RequestACME(cfg ACMEConfig, domains []string) (*ACMEResult, error) {
	if cfg.Challenger == nil {
		return nil, errors.New("cert: ACMEConfig.Challenger is required")
	}
	if len(domains) == 0 {
		return nil, errors.New("cert: at least one domain required")
	}
	if cfg.ContactEmail == "" {
		return nil, errors.New("cert: ACMEConfig.ContactEmail is required")
	}

	accountKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("cert/acme: gen account key: %w", err)
	}
	user := &acmeUser{
		email:      cfg.ContactEmail,
		privateKey: accountKey,
	}

	legoCfg := lego.NewConfig(user)
	if cfg.DirectoryURL != "" {
		legoCfg.CADirURL = cfg.DirectoryURL
	}
	legoCfg.Certificate.KeyType = certcrypto.EC256

	client, err := lego.NewClient(legoCfg)
	if err != nil {
		return nil, fmt.Errorf("cert/acme: new client: %w", err)
	}

	if err := client.Challenge.SetHTTP01Provider(cfg.Challenger); err != nil {
		return nil, fmt.Errorf("cert/acme: set http01 provider: %w", err)
	}

	reg, err := client.Registration.Register(registration.RegisterOptions{TermsOfServiceAgreed: true})
	if err != nil {
		return nil, fmt.Errorf("cert/acme: register: %w", err)
	}
	user.registration = reg

	resource, err := client.Certificate.Obtain(certificate.ObtainRequest{
		Domains: domains,
		Bundle:  true,
	})
	if err != nil {
		return nil, fmt.Errorf("cert/acme: obtain: %w", err)
	}

	// Parse the leaf cert to discover NotAfter.
	block, _ := pem.Decode(resource.Certificate)
	if block == nil {
		return nil, errors.New("cert/acme: returned PEM did not decode")
	}
	parsed, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("cert/acme: parse returned cert: %w", err)
	}

	return &ACMEResult{
		DomainNames: domains,
		PublicPEM:   resource.Certificate,
		PrivatePEM:  resource.PrivateKey,
		ExpiresAt:   parsed.NotAfter,
	}, nil
}
