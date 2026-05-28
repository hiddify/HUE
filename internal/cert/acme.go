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
	"strings"
	"time"

	"github.com/go-acme/lego/v4/certcrypto"
	"github.com/go-acme/lego/v4/certificate"
	"github.com/go-acme/lego/v4/challenge"
	"github.com/go-acme/lego/v4/lego"
	"github.com/go-acme/lego/v4/providers/dns/cloudflare"
	"github.com/go-acme/lego/v4/providers/dns/digitalocean"
	"github.com/go-acme/lego/v4/providers/dns/exec"
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
//
// Exactly one challenge method must be supplied: either Challenger
// (HTTP-01) or DNS01Provider (DNS-01). If both are set, DNS-01 wins
// and HTTP-01 is ignored. If neither is set, RequestACME returns an
// error.
type ACMEConfig struct {
	// DirectoryURL is the ACME server endpoint. Empty = production
	// Let's Encrypt. Use lego.LEDirectoryStaging for tests.
	DirectoryURL string
	// ContactEmail goes into the ACME account registration.
	ContactEmail string
	// Challenger is the shared HTTP-01 provider whose Handler the
	// HUE listener exposes. Required when DNS01Provider is nil.
	Challenger *HTTP01Challenger
	// DNS01Provider is an optional DNS-01 challenge solver. When set
	// it takes precedence over Challenger. Build one via
	// NewDNS01Provider; it reads its credentials from environment
	// variables specific to the chosen DNS provider.
	DNS01Provider challenge.Provider
}

// NewDNS01Provider returns a lego challenge.Provider for the named
// DNS service. The provider reads credentials from environment
// variables following lego's convention for each service:
//
//   - "cloudflare"   — CLOUDFLARE_DNS_API_TOKEN (or EMAIL + API_KEY)
//   - "digitalocean" — DO_AUTH_TOKEN
//   - "exec"         — EXEC_PATH (path to a script that sets/unsets TXT records)
//
// For route53 and gcloud, add
// github.com/go-acme/lego/v4/providers/dns/{route53,gcloud} to go.mod,
// import them here, and add the corresponding cases.
//
// Any unrecognised name returns an error listing valid options.
func NewDNS01Provider(name string) (challenge.Provider, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "cloudflare":
		return cloudflare.NewDNSProvider()
	case "digitalocean":
		return digitalocean.NewDNSProvider()
	case "exec":
		return exec.NewDNSProvider()
	default:
		return nil, fmt.Errorf("cert: unknown DNS-01 provider %q; valid: cloudflare, digitalocean, exec", name)
	}
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

// RequestACME drives a single Let's Encrypt issuance for `domains`.
// DNS-01 takes precedence when DNS01Provider is set; otherwise HTTP-01
// via Challenger is used. Exactly one must be supplied.
//
// Generates a fresh ACME account key per call. Renewal is the
// caller's responsibility — RequestACME always obtains a brand-new cert.
func RequestACME(cfg ACMEConfig, domains []string) (*ACMEResult, error) {
	if cfg.DNS01Provider == nil && cfg.Challenger == nil {
		return nil, errors.New("cert: ACMEConfig requires either DNS01Provider or Challenger")
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

	if cfg.DNS01Provider != nil {
		if err := client.Challenge.SetDNS01Provider(cfg.DNS01Provider); err != nil {
			return nil, fmt.Errorf("cert/acme: set dns01 provider: %w", err)
		}
	} else {
		if err := client.Challenge.SetHTTP01Provider(cfg.Challenger); err != nil {
			return nil, fmt.Errorf("cert/acme: set http01 provider: %w", err)
		}
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
