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
)

// NodeServer implements hue.v1.NodeService.
//
// AuthenticateNode and Heartbeat are still Unimplemented (defaults from
// the embedded UnimplementedNodeServiceServer) — the node-bootstrap
// flow design is pending. SyncConfig IS implemented because services
// need it to bootstrap their protocol-specific config from HUE.
type NodeServer struct {
	huev1.UnimplementedNodeServiceServer

	db     *ent.Client
	logger *slog.Logger
}

// SyncConfig returns the abstract key-value config for the requested
// service. The caller (an adapter, e.g. pkg/clients/xray) feeds the
// returned map to its ConfigGenerator to produce the actual
// protocol-specific configuration.
//
// Etag handling:
//   * If req.current_etag matches the stored etag, response carries
//     `changed: false` and an empty config map — the caller's local
//     copy is up-to-date and no work is needed.
//   * Otherwise, response carries the full map and the new etag.
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
	// 304-equivalent: caller already has this version.
	if req.GetCurrentEtag() != "" && req.GetCurrentEtag() == row.ConfigEtag {
		resp.Changed = false
		return resp, nil
	}

	resp.Changed = true
	if row.Config != nil {
		resp.Config = row.Config
	} else {
		resp.Config = map[string]string{}
	}
	return resp, nil
}
