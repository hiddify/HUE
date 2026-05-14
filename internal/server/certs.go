package server

import (
	"context"
	"log/slog"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	huev1 "github.com/hiddify/hue/gen/go/hue/v1"
	"github.com/hiddify/hue/internal/auth"
	"github.com/hiddify/hue/internal/cert"
	"github.com/hiddify/hue/internal/ent"
	entdomaincertificate "github.com/hiddify/hue/internal/ent/domaincertificate"
	"github.com/hiddify/hue/internal/eventstore"
)

// DomainCertificateServer — shared cert store.
//
// Writes (Owner only): AddCertificate + RequestACME validate + AES-GCM
// at-rest the private key. DeleteCertificate removes by id.
//
// Reads (Reseller + Owner): Get/ListCertificates redact the private
// key field unless the caller is Owner.
type DomainCertificateServer struct {
	huev1.UnimplementedDomainCertificateServiceServer

	db               *ent.Client
	events           *eventstore.Store
	logger           *slog.Logger
	acmeChallenger   *cert.HTTP01Challenger
	acmeDirectoryURL string
	acmeContactEmail string
}

func (s *DomainCertificateServer) AddCertificate(ctx context.Context, req *huev1.AddCertificateRequest) (*huev1.DomainCertificate, error) {
	in := req.GetCertificate()
	if in == nil || in.GetPublicKeyPem() == "" || in.GetPrivateKeyPem() == "" {
		return nil, status.Error(codes.InvalidArgument, "certificate.public_key_pem + private_key_pem required")
	}
	c, err := cert.ParseAndValidate([]byte(in.GetPublicKeyPem()), in.GetDomainNames())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "validate: %v", err)
	}

	// Reject if an existing cert covers any of these domains and we're
	// not replacing with another valid (this one already passed
	// validate; ent's unique constraint isn't useful here since one
	// domain may live in multiple SAN lists). For phase 2.5 we let
	// admins layer freely — actual "never weaken" enforcement is
	// follow-up; documented in docs/certificates.md.
	ct, keyID, err := auth.Encrypt([]byte(in.GetPrivateKeyPem()))
	if err != nil {
		return nil, status.Errorf(codes.Internal, "encrypt: %v", err)
	}
	saved, err := s.db.DomainCertificate.Create().
		SetDomainNames(in.GetDomainNames()).
		SetPublicKeyPem(in.GetPublicKeyPem()).
		SetPrivateKeyCiphertext(ct).
		SetPrivateKeyKeyID(keyID).
		SetExpiresAt(c.NotAfter).
		SetIssuer(entdomaincertificate.IssuerImported).
		SetValid(true).
		Save(ctx)
	if err != nil {
		return nil, mapEntError(err)
	}
	if s.events != nil {
		_ = s.events.Append(ctx, eventstore.Event{
			Type:      "cert_added",
			Timestamp: saved.CreatedAt,
			Metadata:  map[string]any{"domains": in.GetDomainNames(), "issuer": "imported"},
		})
	}
	return certToProto(saved, false), nil
}

func (s *DomainCertificateServer) GetCertificate(ctx context.Context, req *huev1.GetCertificateRequest) (*huev1.DomainCertificate, error) {
	domain := req.GetDomain()
	if domain == "" {
		return nil, status.Error(codes.InvalidArgument, "domain is required")
	}
	rows, err := s.db.DomainCertificate.Query().
		Where(entdomaincertificate.Valid(true)).
		All(ctx)
	if err != nil {
		return nil, mapEntError(err)
	}
	for _, c := range rows {
		for _, d := range c.DomainNames {
			if d == domain {
				return certToProto(c, isOwner(ctx)), nil
			}
		}
	}
	return nil, status.Error(codes.NotFound, "no certificate for domain")
}

func (s *DomainCertificateServer) ListCertificates(ctx context.Context, req *huev1.ListCertificatesRequest) (*huev1.ListCertificatesResponse, error) {
	limit := pageLimit(int(req.GetPageSize()))
	q := s.db.DomainCertificate.Query().Limit(limit)
	if req.GetValidOnly() {
		q = q.Where(entdomaincertificate.Valid(true))
	}
	switch req.GetIssuer() {
	case huev1.CertIssuer_CERT_ISSUER_SELF_SIGNED:
		q = q.Where(entdomaincertificate.IssuerEQ(entdomaincertificate.IssuerSelfSigned))
	case huev1.CertIssuer_CERT_ISSUER_ACME:
		q = q.Where(entdomaincertificate.IssuerEQ(entdomaincertificate.IssuerAcme))
	case huev1.CertIssuer_CERT_ISSUER_IMPORTED:
		q = q.Where(entdomaincertificate.IssuerEQ(entdomaincertificate.IssuerImported))
	}
	rows, err := q.All(ctx)
	if err != nil {
		return nil, mapEntError(err)
	}
	owner := isOwner(ctx)
	out := make([]*huev1.DomainCertificate, 0, len(rows))
	for _, c := range rows {
		out = append(out, certToProto(c, owner))
	}
	return &huev1.ListCertificatesResponse{Certificates: out}, nil
}

func (s *DomainCertificateServer) DeleteCertificate(ctx context.Context, req *huev1.DeleteCertificateRequest) (*emptypb.Empty, error) {
	id, err := uuid.Parse(req.GetId())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "id: %v", err)
	}
	if err := s.db.DomainCertificate.DeleteOneID(id).Exec(ctx); err != nil {
		return nil, mapEntError(err)
	}
	return &emptypb.Empty{}, nil
}

// RequestACME drives a single Let's Encrypt issuance over HTTP-01,
// stores the resulting cert (private key AES-GCM-encrypted), and
// returns the saved row.
//
// Returns Unimplemented when ACME wiring is not configured —
// HUE_ACME_CONTACT_EMAIL must be set and the HUE listener must mount
// the challenge handler at /.well-known/acme-challenge/.
func (s *DomainCertificateServer) RequestACME(ctx context.Context, req *huev1.RequestACMERequest) (*huev1.DomainCertificate, error) {
	if s.acmeChallenger == nil || s.acmeContactEmail == "" {
		return nil, status.Error(codes.Unimplemented, "ACME not configured (set HUE_ACME_CONTACT_EMAIL)")
	}
	domains := req.GetDomainNames()
	if len(domains) == 0 {
		return nil, status.Error(codes.InvalidArgument, "domain_names is required")
	}
	contact := s.acmeContactEmail
	if req.GetContactEmail() != "" {
		contact = req.GetContactEmail()
	}

	result, err := cert.RequestACME(cert.ACMEConfig{
		DirectoryURL: s.acmeDirectoryURL,
		ContactEmail: contact,
		Challenger:   s.acmeChallenger,
	}, domains)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "acme: %v", err)
	}

	ciphertext, keyID, err := auth.Encrypt(result.PrivatePEM)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "encrypt private key: %v", err)
	}
	saved, err := s.db.DomainCertificate.Create().
		SetDomainNames(result.DomainNames).
		SetPublicKeyPem(string(result.PublicPEM)).
		SetPrivateKeyCiphertext(ciphertext).
		SetPrivateKeyKeyID(keyID).
		SetExpiresAt(result.ExpiresAt).
		SetIssuer(entdomaincertificate.IssuerAcme).
		SetValid(true).
		Save(ctx)
	if err != nil {
		return nil, mapEntError(err)
	}
	if s.events != nil {
		_ = s.events.Append(ctx, eventstore.Event{
			Type:      "cert_added",
			Timestamp: saved.CreatedAt,
			Metadata:  map[string]any{"domains": result.DomainNames, "issuer": "acme"},
		})
	}
	return certToProto(saved, false), nil
}

func isOwner(ctx context.Context) bool {
	a, ok := auth.FromContext(ctx)
	return ok && a.Kind == auth.PrincipalKindOwner
}
