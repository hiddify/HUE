package server

import (
	"context"
	"log/slog"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	huev1 "github.com/hiddify/hue/gen/go/hue/v1"
	"github.com/hiddify/hue/internal/ent"
	entservice "github.com/hiddify/hue/internal/ent/service"
	entuser "github.com/hiddify/hue/internal/ent/user"
)

// NodeServer implements hue.v1.NodeService.
//
// AuthenticateNode and Heartbeat remain Unimplemented (defaults from
// the embedded UnimplementedNodeServiceServer) — the node-bootstrap
// flow design is pending. SyncConfig IS implemented because services
// need it to bootstrap their protocol-specific config from HUE.
type NodeServer struct {
	huev1.UnimplementedNodeServiceServer

	db     *ent.Client
	logger *slog.Logger
}

// activeUsersCap caps the user list returned by SyncConfig. Per-service
// scoping is a roadmap follow-up; until then this avoids unbounded
// payloads on large deployments.
const activeUsersCap = 10_000

// SyncConfig returns the full config template + variables + active user
// list for the requested service. The caller's renderer
// (pkg/clients/xray etc.) substitutes the template and applies the
// result to the running service.
//
// Etag handling: when req.current_etag matches the stored etag, the
// response carries `changed: false` and empty template/vars/users.
func (s *NodeServer) SyncConfig(ctx context.Context, req *huev1.SyncConfigRequest) (*huev1.SyncConfigResponse, error) {
	id, err := uuid.Parse(req.GetServiceId())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "service_id: %v", err)
	}

	row, err := s.db.Service.Query().Where(entservice.ID(id)).Only(ctx)
	if err != nil {
		return nil, mapEntError(err)
	}

	resp := &huev1.SyncConfigResponse{
		Etag:      row.ConfigEtag,
		UpdatedAt: timestamppb.New(row.UpdatedAt),
	}
	if req.GetCurrentEtag() != "" && req.GetCurrentEtag() == row.ConfigEtag {
		resp.Changed = false
		return resp, nil
	}

	resp.Changed = true
	resp.Template = &huev1.ConfigTemplate{
		Body:   row.ConfigTemplate,
		Format: row.ConfigTemplateFormat,
	}
	if row.ConfigVars != nil {
		resp.Vars = row.ConfigVars
	} else {
		resp.Vars = map[string]string{}
	}

	users, err := s.db.User.Query().
		Where(entuser.StatusEQ(entuser.StatusActive)).
		Limit(activeUsersCap).
		All(ctx)
	if err != nil {
		return nil, mapEntError(err)
	}
	resp.Users = make([]*huev1.ConfigUser, 0, len(users))
	for _, u := range users {
		resp.Users = append(resp.Users, &huev1.ConfigUser{
			Id:        u.ID.String(),
			Username:  u.Username,
			PublicKey: u.PublicKey,
			Groups:    u.Groups,
		})
	}
	return resp, nil
}
