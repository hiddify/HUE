package server

import (
	"log/slog"

	"google.golang.org/grpc"
	grpchealth "google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"

	huev1 "github.com/hiddify/hue/gen/go/hue/v1"
	"github.com/hiddify/hue/internal/auth"
	"github.com/hiddify/hue/internal/ent"
	"github.com/hiddify/hue/internal/eventstore"
	"github.com/hiddify/hue/internal/service"
)

// Deps groups everything the server needs from the outside world.
type Deps struct {
	DB     *ent.Client
	Engine *service.Engine
	Auth   *auth.Authenticator
	Events *eventstore.Store
	Logger *slog.Logger
}

// Server is the bundle of gRPC service implementations registered onto
// the same grpc.Server. Each sub-server is independent and can be
// constructed in isolation for tests.
type Server struct {
	deps   Deps
	Admin  *AdminServer
	Usage  *UsageServer
	Node   *NodeServer
	Health *grpchealth.Server
}

// New constructs the bundle. Callers should then call Register on a
// configured *grpc.Server.
func New(d Deps) *Server {
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	return &Server{
		deps:   d,
		Admin:  &AdminServer{db: d.DB, engine: d.Engine, events: d.Events, logger: d.Logger},
		Usage:  &UsageServer{engine: d.Engine, logger: d.Logger},
		Node:   &NodeServer{db: d.DB, logger: d.Logger},
		Health: grpchealth.NewServer(),
	}
}

// Register installs all HUE services on g.
func (s *Server) Register(g *grpc.Server) {
	huev1.RegisterAdminServiceServer(g, s.Admin)
	huev1.RegisterUsageServiceServer(g, s.Usage)
	huev1.RegisterNodeServiceServer(g, s.Node)
	healthpb.RegisterHealthServer(g, s.Health)
}

// MarkServing tells the standard health service that we're up.
// Call after migrations + dependencies are ready.
func (s *Server) MarkServing() {
	s.Health.SetServingStatus("", healthpb.HealthCheckResponse_SERVING)
	s.Health.SetServingStatus("hue.v1.AdminService", healthpb.HealthCheckResponse_SERVING)
	s.Health.SetServingStatus("hue.v1.UsageService", healthpb.HealthCheckResponse_SERVING)
	s.Health.SetServingStatus("hue.v1.NodeService", healthpb.HealthCheckResponse_SERVING)
}

// Shutdown flips the gRPC health to NOT_SERVING so load balancers stop
// routing traffic before the actual gRPC graceful stop drains.
func (s *Server) Shutdown() {
	s.Health.Shutdown()
}
