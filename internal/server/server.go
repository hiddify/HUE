package server

import (
	"log/slog"

	"google.golang.org/grpc"
	grpchealth "google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"

	huev1 "github.com/hiddify/hue/gen/go/hue/v1"
	"github.com/hiddify/hue/internal/auth"
	"github.com/hiddify/hue/internal/cert"
	"github.com/hiddify/hue/internal/ent"
	"github.com/hiddify/hue/internal/eventstore"
	"github.com/hiddify/hue/internal/service"
)

// Deps groups everything the server bundle needs from the outside.
type Deps struct {
	DB         *ent.Client
	Engine     *service.Engine
	Auth       *auth.Authenticator
	Resellers  *service.ResellerHierarchy
	SigningKey *auth.SigningKeyMaterial
	Lockout    *auth.Lockout
	Events     *eventstore.Store
	Logger     *slog.Logger

	// ACME wiring (optional — RequestACME returns Unimplemented when
	// ACMEChallenger or ACMEContactEmail is empty).
	ACMEChallenger   *cert.HTTP01Challenger
	ACMEDirectoryURL string
	ACMEContactEmail string
}

// Server bundles every gRPC service impl. The phase-2 split:
//
//   * AdminService             — owner only. Nodes + Agents + Events.
//   * ResellerClientService    — reseller (or owner). Clients + UsagePlans + GetAvailableNodesInfo.
//   * ResellerManagementService — reseller (or owner). Sub-reseller CRUD.
//   * AuthAdminService         — owner only. API keys.
//   * AuthService              — anonymous. Login / Refresh / Logout / ChangePassword.
//   * ConfigService            — agent only. SyncConfig / Heartbeat per node.
//   * DomainCertificateService — reseller reads, owner writes.
//   * UsageService             — agent only. Usage reports.
//
// Most non-CRUD methods are stubbed (Unimplemented) during phase 2.1;
// 2.3-2.5 fills them in.
type Server struct {
	deps Deps

	Admin             *AdminServer
	ResellerClients   *ResellerClientServer
	Resellers         *ResellerManagementServer
	AuthAdmin         *AuthAdminServer
	Auth              *AuthServer
	Config            *ConfigServer
	Certs             *DomainCertificateServer
	Usage             *UsageServer
	Health            *grpchealth.Server
}

func New(d Deps) *Server {
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	return &Server{
		deps:            d,
		Admin:           &AdminServer{db: d.DB, events: d.Events, logger: d.Logger},
		ResellerClients: &ResellerClientServer{db: d.DB, scope: d.Resellers, logger: d.Logger},
		Resellers:       &ResellerManagementServer{db: d.DB, scope: d.Resellers, logger: d.Logger},
		AuthAdmin:       &AuthAdminServer{db: d.DB, logger: d.Logger},
		Auth: &AuthServer{
			db:         d.DB,
			authn:      d.Auth,
			signingKey: d.SigningKey,
			lockout:    d.Lockout,
			events:     d.Events,
			logger:     d.Logger,
		},
		Config: &ConfigServer{db: d.DB, logger: d.Logger},
		Certs: &DomainCertificateServer{
			db:               d.DB,
			events:           d.Events,
			logger:           d.Logger,
			acmeChallenger:   d.ACMEChallenger,
			acmeDirectoryURL: d.ACMEDirectoryURL,
			acmeContactEmail: d.ACMEContactEmail,
		},
		Usage:  &UsageServer{engine: d.Engine, logger: d.Logger},
		Health: grpchealth.NewServer(),
	}
}

func (s *Server) Register(g *grpc.Server) {
	huev1.RegisterAdminServiceServer(g, s.Admin)
	huev1.RegisterResellerClientServiceServer(g, s.ResellerClients)
	huev1.RegisterResellerManagementServiceServer(g, s.Resellers)
	huev1.RegisterAuthAdminServiceServer(g, s.AuthAdmin)
	huev1.RegisterAuthServiceServer(g, s.Auth)
	huev1.RegisterConfigServiceServer(g, s.Config)
	huev1.RegisterDomainCertificateServiceServer(g, s.Certs)
	huev1.RegisterUsageServiceServer(g, s.Usage)
	healthpb.RegisterHealthServer(g, s.Health)
}

func (s *Server) MarkServing() {
	s.Health.SetServingStatus("", healthpb.HealthCheckResponse_SERVING)
	for _, svc := range []string{
		"hue.v1.AdminService",
		"hue.v1.ResellerClientService",
		"hue.v1.ResellerManagementService",
		"hue.v1.AuthAdminService",
		"hue.v1.AuthService",
		"hue.v1.ConfigService",
		"hue.v1.DomainCertificateService",
		"hue.v1.UsageService",
	} {
		s.Health.SetServingStatus(svc, healthpb.HealthCheckResponse_SERVING)
	}
}

func (s *Server) Shutdown() { s.Health.Shutdown() }
