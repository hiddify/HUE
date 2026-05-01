package auth

import (
	"context"
	"errors"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/hiddify/hue/internal/ent"
	entapikey "github.com/hiddify/hue/internal/ent/apikey"
)

// Authenticator validates inbound API keys against the api_keys table.
//
// The set of unauthenticated methods is intentionally tiny: the standard
// health check and the node-bootstrap RPC (which exists to exchange a
// node-issuance one-time token for a session token).
type Authenticator struct {
	db *ent.Client

	// AllowMethods is the set of full gRPC method names that bypass auth,
	// e.g. "/grpc.health.v1.Health/Check". Set at construction.
	AllowMethods map[string]struct{}
}

// NewAuthenticator returns an authenticator for the given ent client.
// Unauthenticated methods include the gRPC health service and HUE's
// HealthCheck + NodeService.AuthenticateNode RPCs.
func NewAuthenticator(db *ent.Client) *Authenticator {
	return &Authenticator{
		db: db,
		AllowMethods: map[string]struct{}{
			"/grpc.health.v1.Health/Check":            {},
			"/grpc.health.v1.Health/Watch":            {},
			"/hue.v1.AdminService/HealthCheck":        {},
			"/hue.v1.NodeService/AuthenticateNode":    {},
		},
	}
}

// Authenticate extracts the bearer token from gRPC metadata, looks it up,
// verifies the Argon2id hash, and returns the Actor on success.
func (a *Authenticator) Authenticate(ctx context.Context) (Actor, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return Actor{}, errors.New("missing metadata")
	}
	values := md.Get("authorization")
	if len(values) == 0 {
		// gRPC-gateway forwards the HTTP "Authorization" header lowercased
		// already; this branch is for direct gRPC clients that capitalize.
		values = md.Get("Authorization")
	}
	if len(values) == 0 {
		return Actor{}, errors.New("missing Authorization header")
	}
	const bearerPfx = "Bearer "
	tok := strings.TrimSpace(values[0])
	if !strings.HasPrefix(tok, bearerPfx) {
		return Actor{}, errors.New("Authorization scheme must be Bearer")
	}
	tok = tok[len(bearerPfx):]

	prefix := LookupPrefix(tok)
	if prefix == "" {
		return Actor{}, errors.New("malformed token")
	}

	row, err := a.db.ApiKey.Query().Where(entapikey.Prefix(prefix)).Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return Actor{}, errors.New("unknown token")
		}
		return Actor{}, err
	}
	if row.RevokedAt != nil {
		return Actor{}, errors.New("token revoked")
	}
	if err := VerifyToken(tok, row.Hash); err != nil {
		return Actor{}, errors.New("token mismatch")
	}

	// Best-effort last_used_at update; do not fail the request on error.
	go func(id any) {
		_, _ = a.db.ApiKey.UpdateOneID(row.ID).
			SetLastUsedAt(time.Now().UTC()).
			Save(context.Background())
	}(row.ID)

	ownerID, _ := parseUUID(row.OwnerID)
	return Actor{
		Kind:    ActorKind(row.Kind),
		OwnerID: ownerID,
		KeyID:   row.ID,
	}, nil
}

// UnaryInterceptor wraps a unary RPC with auth. Methods listed in
// AllowMethods bypass auth.
func (a *Authenticator) UnaryInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if _, allowed := a.AllowMethods[info.FullMethod]; allowed {
			return handler(ctx, req)
		}
		actor, err := a.Authenticate(ctx)
		if err != nil {
			return nil, status.Error(codes.Unauthenticated, err.Error())
		}
		return handler(WithActor(ctx, actor), req)
	}
}

// StreamInterceptor is the streaming counterpart.
func (a *Authenticator) StreamInterceptor() grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		if _, allowed := a.AllowMethods[info.FullMethod]; allowed {
			return handler(srv, ss)
		}
		actor, err := a.Authenticate(ss.Context())
		if err != nil {
			return status.Error(codes.Unauthenticated, err.Error())
		}
		return handler(srv, &serverStreamWithCtx{ServerStream: ss, ctx: WithActor(ss.Context(), actor)})
	}
}

type serverStreamWithCtx struct {
	grpc.ServerStream
	ctx context.Context
}

func (s *serverStreamWithCtx) Context() context.Context { return s.ctx }
