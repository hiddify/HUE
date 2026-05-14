package server

import (
	"context"
	"log/slog"

	"github.com/google/uuid"
	entsql "entgo.io/ent/dialect/sql"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	huev1 "github.com/hiddify/hue/gen/go/hue/v1"
	"github.com/hiddify/hue/internal/ent"
	entagent "github.com/hiddify/hue/internal/ent/agent"
	entevent "github.com/hiddify/hue/internal/ent/event"
	entnode "github.com/hiddify/hue/internal/ent/node"
	"github.com/hiddify/hue/internal/eventstore"
)

// AdminServer — Owner-only. Nodes + Agents + Events + Health.
type AdminServer struct {
	huev1.UnimplementedAdminServiceServer

	db     *ent.Client
	events *eventstore.Store
	logger *slog.Logger
}

// HealthCheck — fast Ping-equivalent.
func (s *AdminServer) HealthCheck(ctx context.Context, _ *huev1.HealthCheckRequest) (*huev1.HealthCheckResponse, error) {
	if _, err := s.db.ApiKey.Query().Limit(1).Count(ctx); err != nil {
		return &huev1.HealthCheckResponse{Status: huev1.HealthStatus_HEALTH_STATUS_NOT_SERVING}, nil
	}
	return &huev1.HealthCheckResponse{Status: huev1.HealthStatus_HEALTH_STATUS_SERVING}, nil
}

// ---------- Nodes ----------

func (s *AdminServer) CreateNode(ctx context.Context, req *huev1.CreateNodeRequest) (*huev1.Node, error) {
	in := req.GetNode()
	if in == nil || in.GetName() == "" {
		return nil, status.Error(codes.InvalidArgument, "node.name is required")
	}
	c := s.db.Node.Create().SetName(in.GetName())
	if v := in.GetIps(); len(v) > 0 {
		c.SetIps(v)
	}
	if v := in.GetAllowedCidrs(); len(v) > 0 {
		c.SetAllowedCidrs(v)
	}
	if v := in.GetTrafficMultiplier(); v > 0 {
		c.SetTrafficMultiplier(v)
	}
	if v := in.GetBandwidthLimitBytes(); v > 0 {
		c.SetBandwidthLimitBytes(v)
	}
	if v := in.GetServiceHostnames(); len(v) > 0 {
		c.SetServiceHostnames(v)
	}
	if v := in.GetConfig(); len(v) > 0 {
		c.SetConfig(structpbToMap(v))
	}
	if g := in.GetGeo(); g != nil {
		if g.GetCountry() != "" {
			c.SetCountry(g.GetCountry())
		}
		if g.GetCity() != "" {
			c.SetCity(g.GetCity())
		}
		if g.GetIsp() != "" {
			c.SetIsp(g.GetIsp())
		}
	}
	if in.GetStatus() == huev1.NodeStatus_NODE_STATUS_DISABLED {
		c.SetStatus(entnode.StatusDisabled)
	}
	saved, err := c.Save(ctx)
	if err != nil {
		return nil, mapEntError(err)
	}
	return nodeToProto(saved), nil
}

func (s *AdminServer) GetNode(ctx context.Context, req *huev1.GetNodeRequest) (*huev1.Node, error) {
	id, err := uuid.Parse(req.GetId())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "id: %v", err)
	}
	n, err := s.db.Node.Get(ctx, id)
	if err != nil {
		return nil, mapEntError(err)
	}
	return nodeToProto(n), nil
}

func (s *AdminServer) ListNodes(ctx context.Context, req *huev1.ListNodesRequest) (*huev1.ListNodesResponse, error) {
	limit := pageLimit(int(req.GetPageSize()))
	q := s.db.Node.Query().Limit(limit)
	if st := req.GetStatus(); st != huev1.NodeStatus_NODE_STATUS_UNSPECIFIED {
		q = q.Where(entnode.StatusEQ(nodeStatusToEnt(st)))
	}
	rows, err := q.All(ctx)
	if err != nil {
		return nil, mapEntError(err)
	}
	out := make([]*huev1.Node, 0, len(rows))
	for _, n := range rows {
		out = append(out, nodeToProto(n))
	}
	return &huev1.ListNodesResponse{Nodes: out}, nil
}

func (s *AdminServer) UpdateNode(ctx context.Context, req *huev1.UpdateNodeRequest) (*huev1.Node, error) {
	id, err := uuid.Parse(req.GetId())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "id: %v", err)
	}
	in := req.GetNode()
	if in == nil {
		return nil, status.Error(codes.InvalidArgument, "node is required")
	}
	upd := s.db.Node.UpdateOneID(id)
	if v := in.GetName(); v != "" {
		upd.SetName(v)
	}
	if v := in.GetIps(); v != nil {
		upd.SetIps(v)
	}
	if v := in.GetAllowedCidrs(); v != nil {
		upd.SetAllowedCidrs(v)
	}
	if v := in.GetTrafficMultiplier(); v > 0 {
		upd.SetTrafficMultiplier(v)
	}
	if v := in.GetBandwidthLimitBytes(); v > 0 {
		upd.SetBandwidthLimitBytes(v)
	}
	if v := in.GetServiceHostnames(); v != nil {
		upd.SetServiceHostnames(v)
	}
	if v := in.GetConfig(); v != nil {
		upd.SetConfig(structpbToMap(v))
	}
	if in.GetStatus() != huev1.NodeStatus_NODE_STATUS_UNSPECIFIED {
		upd.SetStatus(nodeStatusToEnt(in.GetStatus()))
	}
	saved, err := upd.Save(ctx)
	if err != nil {
		return nil, mapEntError(err)
	}
	return nodeToProto(saved), nil
}

func (s *AdminServer) DeleteNode(ctx context.Context, req *huev1.DeleteNodeRequest) (*emptypb.Empty, error) {
	id, err := uuid.Parse(req.GetId())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "id: %v", err)
	}
	if err := s.db.Node.DeleteOneID(id).Exec(ctx); err != nil {
		return nil, mapEntError(err)
	}
	return &emptypb.Empty{}, nil
}

// ---------- Agents ----------

func (s *AdminServer) CreateAgent(ctx context.Context, req *huev1.CreateAgentRequest) (*huev1.CreateAgentResponse, error) {
	in := req.GetAgent()
	if in == nil || in.GetName() == "" {
		return nil, status.Error(codes.InvalidArgument, "agent.name is required")
	}
	nodeID, err := uuid.Parse(in.GetNodeId())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "node_id: %v", err)
	}
	kind, ok := agentKindToEnt(in.GetKind())
	if !ok {
		return nil, status.Error(codes.InvalidArgument, "agent.kind is required")
	}
	c := s.db.Agent.Create().SetNodeID(nodeID).SetName(in.GetName()).SetKind(kind)
	if v := in.GetVersion(); v != "" {
		c.SetVersion(v)
	}
	saved, err := c.Save(ctx)
	if err != nil {
		return nil, mapEntError(err)
	}
	// API-key issuance — proto contract returns the plaintext token once.
	// Real implementation (Argon2id hash + insert into api_keys row tied
	// to this Agent.id) lands alongside AuthAdminService in 2.2; until
	// then the field is empty and operators mint the key via
	// AuthAdminService.CreateApiKey.
	return &huev1.CreateAgentResponse{Agent: agentToProto(saved)}, nil
}

func (s *AdminServer) GetAgent(ctx context.Context, req *huev1.GetAgentRequest) (*huev1.Agent, error) {
	id, err := uuid.Parse(req.GetId())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "id: %v", err)
	}
	a, err := s.db.Agent.Get(ctx, id)
	if err != nil {
		return nil, mapEntError(err)
	}
	return agentToProto(a), nil
}

func (s *AdminServer) ListAgents(ctx context.Context, req *huev1.ListAgentsRequest) (*huev1.ListAgentsResponse, error) {
	limit := pageLimit(int(req.GetPageSize()))
	q := s.db.Agent.Query().Limit(limit)
	if v := req.GetNodeId(); v != "" {
		if id, err := uuid.Parse(v); err == nil {
			q = q.Where(entagent.NodeID(id))
		}
	}
	if k, ok := agentKindToEnt(req.GetKind()); ok {
		q = q.Where(entagent.KindEQ(k))
	}
	rows, err := q.All(ctx)
	if err != nil {
		return nil, mapEntError(err)
	}
	out := make([]*huev1.Agent, 0, len(rows))
	for _, a := range rows {
		out = append(out, agentToProto(a))
	}
	return &huev1.ListAgentsResponse{Agents: out}, nil
}

func (s *AdminServer) UpdateAgent(ctx context.Context, req *huev1.UpdateAgentRequest) (*huev1.Agent, error) {
	id, err := uuid.Parse(req.GetId())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "id: %v", err)
	}
	in := req.GetAgent()
	if in == nil {
		return nil, status.Error(codes.InvalidArgument, "agent is required")
	}
	upd := s.db.Agent.UpdateOneID(id)
	if v := in.GetName(); v != "" {
		upd.SetName(v)
	}
	if v := in.GetVersion(); v != "" {
		upd.SetVersion(v)
	}
	saved, err := upd.Save(ctx)
	if err != nil {
		return nil, mapEntError(err)
	}
	return agentToProto(saved), nil
}

func (s *AdminServer) DeleteAgent(ctx context.Context, req *huev1.DeleteAgentRequest) (*emptypb.Empty, error) {
	id, err := uuid.Parse(req.GetId())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "id: %v", err)
	}
	if err := s.db.Agent.DeleteOneID(id).Exec(ctx); err != nil {
		return nil, mapEntError(err)
	}
	return &emptypb.Empty{}, nil
}

// ---------- Events ----------

func (s *AdminServer) ListEvents(ctx context.Context, req *huev1.ListEventsRequest) (*huev1.ListEventsResponse, error) {
	limit := pageLimit(int(req.GetPageSize()))
	q := s.db.Event.Query().Order(entevent.ByTs(entsql.OrderDesc())).Limit(limit)
	if t := req.GetType(); t != huev1.EventType_EVENT_TYPE_UNSPECIFIED {
		if s := EventTypeToString(t); s != "" {
			q = q.Where(entevent.TypeEQ(entevent.Type(s)))
		}
	}
	if v := req.GetClientId(); v != "" {
		q = q.Where(entevent.ClientIDEQ(v))
	}
	if v := req.GetResellerId(); v != "" {
		q = q.Where(entevent.ResellerIDEQ(v))
	}
	if t := req.GetSince(); t != nil {
		q = q.Where(entevent.TsGTE(t.AsTime()))
	}
	if t := req.GetUntil(); t != nil {
		q = q.Where(entevent.TsLTE(t.AsTime()))
	}
	rows, err := q.All(ctx)
	if err != nil {
		return nil, mapEntError(err)
	}
	out := make([]*huev1.Event, 0, len(rows))
	for _, r := range rows {
		out = append(out, entEventToProto(r))
	}
	return &huev1.ListEventsResponse{Events: out}, nil
}

func (s *AdminServer) StreamEvents(req *huev1.StreamEventsRequest, stream grpc.ServerStreamingServer[huev1.Event]) error {
	filter := eventstore.Filter{
		ClientID:   req.GetClientId(),
		ResellerID: req.GetResellerId(),
	}
	for _, t := range req.GetTypes() {
		if s := EventTypeToString(t); s != "" {
			filter.Types = append(filter.Types, s)
		}
	}
	_, ch, unsub := s.events.Subscribe(filter, int(req.GetBufferSize()))
	defer unsub()
	ctx := stream.Context()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case e, ok := <-ch:
			if !ok {
				return nil
			}
			if err := stream.Send(eventFromStore(e)); err != nil {
				return err
			}
		}
	}
}

// Helper

func pageLimit(req int) int {
	if req <= 0 || req > 500 {
		return 100
	}
	return req
}
