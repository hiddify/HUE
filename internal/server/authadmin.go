package server

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	huev1 "github.com/hiddify/hue/gen/go/hue/v1"
	"github.com/hiddify/hue/internal/auth"
	"github.com/hiddify/hue/internal/ent"
	entapikey "github.com/hiddify/hue/internal/ent/apikey"
)

// AuthAdminServer — Owner-only. API key issuance / listing / revocation.
type AuthAdminServer struct {
	huev1.UnimplementedAuthAdminServiceServer

	db     *ent.Client
	logger *slog.Logger
}

func (s *AuthAdminServer) CreateApiKey(ctx context.Context, req *huev1.CreateApiKeyRequest) (*huev1.CreateApiKeyResponse, error) {
	in := req.GetApiKey()
	if in == nil || in.GetName() == "" {
		return nil, status.Error(codes.InvalidArgument, "api_key.name is required")
	}
	var kind auth.ActorKind
	switch in.GetKind() {
	case huev1.ApiKeyKind_API_KEY_KIND_OWNER:
		kind = auth.KindOwner
	case huev1.ApiKeyKind_API_KEY_KIND_AGENT:
		kind = auth.KindAgent
		if in.GetAgentId() == "" {
			return nil, status.Error(codes.InvalidArgument, "agent_id is required for AGENT keys")
		}
	default:
		return nil, status.Error(codes.InvalidArgument, "kind is required")
	}
	prefix, plaintext, hash, err := auth.GenerateKey(kind)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "generate key: %v", err)
	}
	c := s.db.ApiKey.Create().
		SetKind(entApiKeyKind(kind)).
		SetName(in.GetName()).
		SetPrefix(prefix).
		SetHash(hash)
	if v := in.GetAgentId(); v != "" {
		c.SetAgentID(v)
	}
	if exp := in.GetExpiresAt(); exp != nil && exp.IsValid() {
		c.SetExpiresAt(exp.AsTime())
	}
	saved, err := c.Save(ctx)
	if err != nil {
		return nil, mapEntError(err)
	}
	return &huev1.CreateApiKeyResponse{ApiKey: apiKeyToProto(saved), Token: plaintext}, nil
}

func (s *AuthAdminServer) ListApiKeys(ctx context.Context, req *huev1.ListApiKeysRequest) (*huev1.ListApiKeysResponse, error) {
	limit := pageLimit(int(req.GetPageSize()))
	q := s.db.ApiKey.Query().Limit(limit)
	switch req.GetKind() {
	case huev1.ApiKeyKind_API_KEY_KIND_OWNER:
		q = q.Where(entapikey.KindEQ(entapikey.KindOwner))
	case huev1.ApiKeyKind_API_KEY_KIND_AGENT:
		q = q.Where(entapikey.KindEQ(entapikey.KindAgent))
	}
	if v := req.GetAgentId(); v != "" {
		q = q.Where(entapikey.AgentID(v))
	}
	rows, err := q.All(ctx)
	if err != nil {
		return nil, mapEntError(err)
	}
	out := make([]*huev1.ApiKey, 0, len(rows))
	for _, k := range rows {
		out = append(out, apiKeyToProto(k))
	}
	return &huev1.ListApiKeysResponse{ApiKeys: out}, nil
}

func (s *AuthAdminServer) RevokeApiKey(ctx context.Context, req *huev1.RevokeApiKeyRequest) (*huev1.ApiKey, error) {
	id, err := uuid.Parse(req.GetId())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "id: %v", err)
	}
	saved, err := s.db.ApiKey.UpdateOneID(id).SetRevokedAt(time.Now().UTC()).Save(ctx)
	if err != nil {
		return nil, mapEntError(err)
	}
	return apiKeyToProto(saved), nil
}

func entApiKeyKind(k auth.ActorKind) entapikey.Kind {
	if k == auth.KindAgent {
		return entapikey.KindAgent
	}
	return entapikey.KindOwner
}

// _ uses ent package; keeps import non-dead if a future helper drops out.
var _ = ent.IsNotFound
