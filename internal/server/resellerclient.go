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
	"github.com/hiddify/hue/internal/ent"
	entnode "github.com/hiddify/hue/internal/ent/node"
	entsubscriber "github.com/hiddify/hue/internal/ent/subscriber"
	entusageplan "github.com/hiddify/hue/internal/ent/usageplan"
	"github.com/hiddify/hue/internal/service"
)

// ResellerClientServer — Reseller (or Owner) manages their Clients +
// reads UsagePlans + sees available nodes.
//
// Client passwords are AES-256-GCM encrypted at rest (auth.Encrypt).
// Empty passwords are stored empty (clients with non-password auth only).
type ResellerClientServer struct {
	huev1.UnimplementedResellerClientServiceServer

	db     *ent.Client
	scope  *service.ResellerHierarchy
	logger *slog.Logger
}

// allowedResellerIDs is the set of reseller IDs the caller can act on.
// Returns (nil, nil) for Owner — owner sees everything, no filter needed.
// Returns the descendant set when the caller is a Reseller.
func (s *ResellerClientServer) allowedResellerIDs(ctx context.Context) (map[uuid.UUID]struct{}, error) {
	actor, ok := auth.FromContext(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "missing principal")
	}
	if actor.Kind == auth.PrincipalKindOwner {
		return nil, nil
	}
	if actor.Kind != auth.PrincipalKindReseller {
		return nil, status.Error(codes.PermissionDenied, "kind cannot manage clients")
	}
	if s.scope == nil {
		return nil, status.Error(codes.Internal, "reseller scope unset")
	}
	return s.scope.DescendantIDs(ctx, actor.SubjectID)
}

// inScope reports whether resellerID falls within the calling
// reseller's allowed subtree. Owner = always.
func (s *ResellerClientServer) inScope(allowed map[uuid.UUID]struct{}, resellerID *uuid.UUID) bool {
	if allowed == nil {
		return true
	}
	if resellerID == nil {
		// Client without a reseller assignment — only Owner sees these.
		return false
	}
	_, ok := allowed[*resellerID]
	return ok
}

func (s *ResellerClientServer) CreateClient(ctx context.Context, req *huev1.CreateClientRequest) (*huev1.Client, error) {
	in := req.GetClient()
	if in == nil || in.GetAuthMethod() == nil {
		return nil, status.Error(codes.InvalidArgument, "client.auth_method.username is required")
	}
	username := in.GetAuthMethod().GetUsername()
	if username == "" {
		return nil, status.Error(codes.InvalidArgument, "username is required")
	}
	allowed, err := s.allowedResellerIDs(ctx)
	if err != nil {
		return nil, err
	}
	// For Reseller callers, the new client MUST be assigned a reseller
	// in the caller's subtree. Default to the caller's own id when
	// none provided.
	if allowed != nil {
		actor, _ := auth.FromContext(ctx)
		if in.GetInfo() == nil {
			in.Info = &huev1.ClientInfo{}
		}
		if in.GetInfo().GetResellerId() == "" {
			in.GetInfo().ResellerId = actor.SubjectID.String()
		} else if parsed, err := uuid.Parse(in.GetInfo().GetResellerId()); err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "reseller_id: %v", err)
		} else if _, ok := allowed[parsed]; !ok {
			return nil, status.Error(codes.PermissionDenied, "reseller_id outside caller's subtree")
		}
	}
	create := s.db.Subscriber.Create().SetUsername(username)
	if pw := in.GetAuthMethod().GetPassword(); pw != "" {
		ct, keyID, err := auth.Encrypt([]byte(pw))
		if err != nil {
			return nil, status.Errorf(codes.Internal, "encrypt password: %v", err)
		}
		create.SetPasswordCiphertext(ct).SetPasswordKeyID(keyID)
	}
	if v := in.GetAuthMethod().GetAllowedDevices(); len(v) > 0 {
		create.SetAllowedDevices(v)
	}
	if v := in.GetAuthMethod().GetPublicKey(); v != "" {
		create.SetPublicKey(v)
	}
	if info := in.GetInfo(); info != nil {
		if v := info.GetGroups(); len(v) > 0 {
			create.SetGroups(v)
		}
		if info.GetStatus() != huev1.ClientStatus_CLIENT_STATUS_UNSPECIFIED {
			create.SetStatus(clientStatusToEnt(info.GetStatus()))
		}
		if mid := info.GetResellerId(); mid != "" {
			parsed, err := uuid.Parse(mid)
			if err != nil {
				return nil, status.Errorf(codes.InvalidArgument, "reseller_id: %v", err)
			}
			create.SetResellerID(parsed)
		}
	}
	saved, err := create.Save(ctx)
	if err != nil {
		return nil, mapEntError(err)
	}
	return subscriberToProtoClient(saved), nil
}

func (s *ResellerClientServer) GetClient(ctx context.Context, req *huev1.GetClientRequest) (*huev1.Client, error) {
	id, err := uuid.Parse(req.GetId())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "id: %v", err)
	}
	allowed, err := s.allowedResellerIDs(ctx)
	if err != nil {
		return nil, err
	}
	row, err := s.db.Subscriber.Query().
		Where(entsubscriber.ID(id)).
		WithActivePlan().
		Only(ctx)
	if err != nil {
		return nil, mapEntError(err)
	}
	if !s.inScope(allowed, row.ResellerID) {
		return nil, status.Error(codes.NotFound, "client not found")
	}
	return subscriberToProtoClient(row), nil
}

func (s *ResellerClientServer) ListClients(ctx context.Context, req *huev1.ListClientsRequest) (*huev1.ListClientsResponse, error) {
	limit := pageLimit(int(req.GetPageSize()))
	allowed, err := s.allowedResellerIDs(ctx)
	if err != nil {
		return nil, err
	}
	q := s.db.Subscriber.Query().WithActivePlan().Limit(limit)
	if st := req.GetStatus(); st != huev1.ClientStatus_CLIENT_STATUS_UNSPECIFIED {
		q = q.Where(entsubscriber.StatusEQ(clientStatusToEnt(st)))
	}
	if mid := req.GetResellerId(); mid != "" {
		id, err := uuid.Parse(mid)
		if err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "reseller_id: %v", err)
		}
		if allowed != nil {
			if _, ok := allowed[id]; !ok {
				return nil, status.Error(codes.PermissionDenied, "reseller_id outside caller's subtree")
			}
		}
		q = q.Where(entsubscriber.ResellerID(id))
	} else if allowed != nil {
		// Restrict to subtree.
		ids := make([]uuid.UUID, 0, len(allowed))
		for id := range allowed {
			ids = append(ids, id)
		}
		q = q.Where(entsubscriber.ResellerIDIn(ids...))
	}
	rows, err := q.All(ctx)
	if err != nil {
		return nil, mapEntError(err)
	}
	out := make([]*huev1.Client, 0, len(rows))
	for _, r := range rows {
		out = append(out, subscriberToProtoClient(r))
	}
	return &huev1.ListClientsResponse{Clients: out, Total: int32(len(out))}, nil
}

func (s *ResellerClientServer) UpdateClient(ctx context.Context, req *huev1.UpdateClientRequest) (*huev1.Client, error) {
	id, err := uuid.Parse(req.GetId())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "id: %v", err)
	}
	in := req.GetClient()
	if in == nil {
		return nil, status.Error(codes.InvalidArgument, "client is required")
	}
	allowed, err := s.allowedResellerIDs(ctx)
	if err != nil {
		return nil, err
	}
	existing, err := s.db.Subscriber.Get(ctx, id)
	if err != nil {
		return nil, mapEntError(err)
	}
	if !s.inScope(allowed, existing.ResellerID) {
		return nil, status.Error(codes.NotFound, "client not found")
	}
	upd := s.db.Subscriber.UpdateOneID(id)
	if am := in.GetAuthMethod(); am != nil {
		if v := am.GetUsername(); v != "" {
			upd.SetUsername(v)
		}
		if v := am.GetPassword(); v != "" {
			ct, keyID, err := auth.Encrypt([]byte(v))
			if err != nil {
				return nil, status.Errorf(codes.Internal, "encrypt password: %v", err)
			}
			upd.SetPasswordCiphertext(ct).SetPasswordKeyID(keyID)
		}
		if v := am.GetAllowedDevices(); v != nil {
			upd.SetAllowedDevices(v)
		}
		if v := am.GetPublicKey(); v != "" {
			upd.SetPublicKey(v)
		}
	}
	if info := in.GetInfo(); info != nil {
		if info.GetStatus() != huev1.ClientStatus_CLIENT_STATUS_UNSPECIFIED {
			upd.SetStatus(clientStatusToEnt(info.GetStatus()))
		}
		if v := info.GetGroups(); v != nil {
			upd.SetGroups(v)
		}
	}
	saved, err := upd.Save(ctx)
	if err != nil {
		return nil, mapEntError(err)
	}
	return subscriberToProtoClient(saved), nil
}

func (s *ResellerClientServer) DeleteClient(ctx context.Context, req *huev1.DeleteClientRequest) (*emptypb.Empty, error) {
	id, err := uuid.Parse(req.GetId())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "id: %v", err)
	}
	allowed, err := s.allowedResellerIDs(ctx)
	if err != nil {
		return nil, err
	}
	existing, err := s.db.Subscriber.Get(ctx, id)
	if err != nil {
		return nil, mapEntError(err)
	}
	if !s.inScope(allowed, existing.ResellerID) {
		return nil, status.Error(codes.NotFound, "client not found")
	}
	if err := s.db.Subscriber.DeleteOneID(id).Exec(ctx); err != nil {
		return nil, mapEntError(err)
	}
	return &emptypb.Empty{}, nil
}

// ---------- Usage plans (read-only here; mutation via engine) ----------

func (s *ResellerClientServer) GetUsagePlan(ctx context.Context, req *huev1.GetUsagePlanRequest) (*huev1.UsagePlan, error) {
	id, err := uuid.Parse(req.GetId())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "id: %v", err)
	}
	p, err := s.db.UsagePlan.Get(ctx, id)
	if err != nil {
		return nil, mapEntError(err)
	}
	return usagePlanToProto(p), nil
}

func (s *ResellerClientServer) ListUsagePlans(ctx context.Context, req *huev1.ListUsagePlansRequest) (*huev1.ListUsagePlansResponse, error) {
	limit := pageLimit(int(req.GetPageSize()))
	q := s.db.UsagePlan.Query().Limit(limit)
	if v := req.GetClientId(); v != "" {
		if id, err := uuid.Parse(v); err == nil {
			q = q.Where(entusageplan.ClientID(id))
		}
	}
	rows, err := q.All(ctx)
	if err != nil {
		return nil, mapEntError(err)
	}
	out := make([]*huev1.UsagePlan, 0, len(rows))
	for _, p := range rows {
		out = append(out, usagePlanToProto(p))
	}
	return &huev1.ListUsagePlansResponse{Plans: out}, nil
}

func (s *ResellerClientServer) GetActiveUsagePlan(ctx context.Context, req *huev1.GetActiveUsagePlanRequest) (*huev1.UsagePlan, error) {
	id, err := uuid.Parse(req.GetClientId())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "client_id: %v", err)
	}
	row, err := s.db.Subscriber.Query().
		Where(entsubscriber.ID(id)).
		WithActivePlan().
		Only(ctx)
	if err != nil {
		return nil, mapEntError(err)
	}
	plan := row.Edges.ActivePlan
	if plan == nil {
		return nil, status.Error(codes.NotFound, "client has no active plan")
	}
	return usagePlanToProto(plan), nil
}

func (s *ResellerClientServer) GetAvailableNodesInfo(ctx context.Context, _ *huev1.GetAvailableNodesInfoRequest) (*huev1.GetAvailableNodesInfoResponse, error) {
	rows, err := s.db.Node.Query().
		Where(entnode.StatusEQ(entnode.StatusActive)).
		Limit(500).
		All(ctx)
	if err != nil {
		return nil, mapEntError(err)
	}
	out := make([]*huev1.NodeInfo, 0, len(rows))
	for _, n := range rows {
		out = append(out, &huev1.NodeInfo{
			Id:   n.ID.String(),
			Name: n.Name,
			Geo:  &huev1.Geo{Country: n.Country, City: n.City, Isp: n.Isp, Asn: n.Asn},
		})
	}
	return &huev1.GetAvailableNodesInfoResponse{Nodes: out}, nil
}
