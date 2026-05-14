package auth

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/hiddify/hue/internal/ent"
	entapikey "github.com/hiddify/hue/internal/ent/apikey"
)

// Authenticator validates inbound API keys against the api_keys table
// and (in phase 2.3) verifies JWTs for Subscriber/Reseller principals.
//
// API-key kinds today: Owner, Agent.
// JWT kinds (handled by JWTVerifier — wired in phase 2.3): Subscriber,
// Reseller.
//
// AllowMethods bypasses auth — kept tiny: gRPC health + HUE's own
// HealthCheck + anonymous AuthService RPCs (Login / Refresh).
type Authenticator struct {
	db *ent.Client

	AllowMethods map[string]struct{}
}

func NewAuthenticator(db *ent.Client) *Authenticator {
	return &Authenticator{
		db: db,
		AllowMethods: map[string]struct{}{
			"/grpc.health.v1.Health/Check":         {},
			"/grpc.health.v1.Health/Watch":         {},
			"/hue.v1.AdminService/HealthCheck":     {},
			"/hue.v1.AuthService/Login":            {},
			"/hue.v1.AuthService/Refresh":          {},
		},
	}
}

// Authenticate extracts the bearer token from gRPC metadata, decides
// whether it's an API key (Owner/Agent) or a JWT (Subscriber/Reseller)
// by shape, validates accordingly, and returns the Actor.
//
// API key shape: starts with "own_" or "agt_".
// JWT shape: three base64url-encoded segments separated by ".".
func (a *Authenticator) Authenticate(ctx context.Context) (Actor, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return Actor{}, errors.New("missing metadata")
	}
	values := md.Get("authorization")
	if len(values) == 0 {
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

	if looksLikeAPIKey(tok) {
		return a.authenticateAPIKey(ctx, tok)
	}
	if looksLikeJWT(tok) {
		return a.authenticateJWT(ctx, tok)
	}
	return Actor{}, errors.New("malformed token")
}

func looksLikeAPIKey(tok string) bool {
	return strings.HasPrefix(tok, prefixOwner+"_") || strings.HasPrefix(tok, prefixAgent+"_")
}

func looksLikeJWT(tok string) bool {
	// Three dot-separated segments and no underscores in the first one
	// (rules out our own API-key shape's prefix).
	return strings.Count(tok, ".") == 2 && !strings.Contains(strings.SplitN(tok, ".", 2)[0], "_")
}

func (a *Authenticator) authenticateAPIKey(ctx context.Context, tok string) (Actor, error) {
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
	if row.ExpiresAt != nil && !row.ExpiresAt.After(time.Now()) {
		return Actor{}, errors.New("token expired")
	}
	if err := VerifyToken(tok, row.Hash); err != nil {
		return Actor{}, errors.New("token mismatch")
	}
	go func() {
		_, _ = a.db.ApiKey.UpdateOneID(row.ID).
			SetLastUsedAt(time.Now().UTC()).
			Save(context.Background())
	}()

	var (
		kind      PrincipalKind
		subjectID uuid.UUID
	)
	switch row.Kind {
	case entapikey.KindOwner:
		kind = PrincipalKindOwner
	case entapikey.KindAgent:
		kind = PrincipalKindAgent
		subjectID, _ = parseUUID(row.AgentID)
	default:
		return Actor{}, errors.New("unknown api_key kind")
	}
	return Actor{
		Kind:      kind,
		SubjectID: subjectID,
		KeyID:     row.ID,
	}, nil
}

func (a *Authenticator) authenticateJWT(ctx context.Context, tok string) (Actor, error) {
	claims, err := VerifyJWT(ctx, a.db, tok)
	if err != nil {
		return Actor{}, err
	}
	var kind PrincipalKind
	switch claims.Kind {
	case "subscriber":
		kind = PrincipalKindSubscriber
	case "reseller":
		kind = PrincipalKindReseller
	case "owner":
		kind = PrincipalKindOwner
	default:
		return Actor{}, errors.New("unknown JWT kind")
	}
	subjectID, _ := parseUUID(claims.Subject)
	sudoTargetID, _ := parseUUID(claims.SudoTargetID)
	return Actor{
		Kind:         kind,
		SubjectID:    subjectID,
		SudoTargetID: sudoTargetID,
	}, nil
}

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
