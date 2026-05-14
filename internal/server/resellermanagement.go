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
	entreseller "github.com/hiddify/hue/internal/ent/reseller"
	"github.com/hiddify/hue/internal/service"
)

// ResellerManagementServer — sub-reseller CRUD. Scope (recursive
// descendants-only) is enforced by the auth interceptor in phase 2.3
// once positive authorization wraps in; for now, RBAC checks are
// trivial (Owner can do anything, others rely on the interceptor's
// kind whitelist).
//
// Reseller passwords are Argon2id-hashed via auth.HashToken on
// Create / Update / ChangePassword. Plaintext is never persisted.
type ResellerManagementServer struct {
	huev1.UnimplementedResellerManagementServiceServer

	db     *ent.Client
	scope  *service.ResellerHierarchy
	logger *slog.Logger
}

// allowedResellerIDs returns the descendant set for a Reseller caller,
// nil for Owner (= no filter).
func (s *ResellerManagementServer) allowedResellerIDs(ctx context.Context) (map[uuid.UUID]struct{}, error) {
	actor, ok := auth.FromContext(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "missing principal")
	}
	if actor.Kind == auth.PrincipalKindOwner {
		return nil, nil
	}
	if actor.Kind != auth.PrincipalKindReseller {
		return nil, status.Error(codes.PermissionDenied, "kind cannot manage resellers")
	}
	return s.scope.DescendantIDs(ctx, actor.SubjectID)
}

func (s *ResellerManagementServer) CreateReseller(ctx context.Context, req *huev1.CreateResellerRequest) (*huev1.Reseller, error) {
	in := req.GetReseller()
	if in == nil || in.GetName() == "" {
		return nil, status.Error(codes.InvalidArgument, "reseller.name is required")
	}
	pw := in.GetPassword()
	if pw == "" {
		return nil, status.Error(codes.InvalidArgument, "reseller.password is required")
	}
	allowed, err := s.allowedResellerIDs(ctx)
	if err != nil {
		return nil, err
	}
	// Reseller caller can only create children in their own subtree.
	// Owner can create roots (parent_id empty).
	if allowed != nil {
		actor, _ := auth.FromContext(ctx)
		if in.GetParentId() == "" {
			in.ParentId = actor.SubjectID.String()
		} else if parsed, err := uuid.Parse(in.GetParentId()); err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "parent_id: %v", err)
		} else if _, ok := allowed[parsed]; !ok {
			return nil, status.Error(codes.PermissionDenied, "parent_id outside caller's subtree")
		}
	}
	hash, err := auth.HashToken(pw)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "hash password: %v", err)
	}
	c := s.db.Reseller.Create().
		SetName(in.GetName()).
		SetPasswordHash(hash)
	if v := in.GetDisplayName(); v != "" {
		c.SetDisplayName(v)
	}
	if pid := in.GetParentId(); pid != "" {
		parsed, err := uuid.Parse(pid)
		if err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "parent_id: %v", err)
		}
		c.SetParentID(parsed)
	}
	if in.GetStatus() == huev1.ResellerStatus_RESELLER_STATUS_INACTIVE {
		c.SetStatus(entreseller.StatusInactive)
	}
	saved, err := c.Save(ctx)
	if err != nil {
		return nil, mapEntError(err)
	}
	return resellerToProto(saved), nil
}

func (s *ResellerManagementServer) GetReseller(ctx context.Context, req *huev1.GetResellerRequest) (*huev1.Reseller, error) {
	id, err := uuid.Parse(req.GetId())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "id: %v", err)
	}
	allowed, err := s.allowedResellerIDs(ctx)
	if err != nil {
		return nil, err
	}
	if allowed != nil {
		if _, ok := allowed[id]; !ok {
			return nil, status.Error(codes.NotFound, "reseller not found")
		}
	}
	r, err := s.db.Reseller.Get(ctx, id)
	if err != nil {
		return nil, mapEntError(err)
	}
	return resellerToProto(r), nil
}

func (s *ResellerManagementServer) ListResellers(ctx context.Context, req *huev1.ListResellersRequest) (*huev1.ListResellersResponse, error) {
	limit := pageLimit(int(req.GetPageSize()))
	allowed, err := s.allowedResellerIDs(ctx)
	if err != nil {
		return nil, err
	}
	q := s.db.Reseller.Query().Limit(limit)
	if v := req.GetParentId(); v != "" {
		if id, err := uuid.Parse(v); err == nil {
			q = q.Where(entreseller.ParentID(id))
		}
	}
	if req.GetStatus() != huev1.ResellerStatus_RESELLER_STATUS_UNSPECIFIED {
		q = q.Where(entreseller.StatusEQ(resellerStatusToEnt(req.GetStatus())))
	}
	if allowed != nil {
		ids := make([]uuid.UUID, 0, len(allowed))
		for id := range allowed {
			ids = append(ids, id)
		}
		q = q.Where(entreseller.IDIn(ids...))
	}
	rows, err := q.All(ctx)
	if err != nil {
		return nil, mapEntError(err)
	}
	out := make([]*huev1.Reseller, 0, len(rows))
	for _, r := range rows {
		out = append(out, resellerToProto(r))
	}
	return &huev1.ListResellersResponse{Resellers: out}, nil
}

func (s *ResellerManagementServer) UpdateReseller(ctx context.Context, req *huev1.UpdateResellerRequest) (*huev1.Reseller, error) {
	id, err := uuid.Parse(req.GetId())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "id: %v", err)
	}
	in := req.GetReseller()
	if in == nil {
		return nil, status.Error(codes.InvalidArgument, "reseller is required")
	}
	allowed, err := s.allowedResellerIDs(ctx)
	if err != nil {
		return nil, err
	}
	if allowed != nil {
		if _, ok := allowed[id]; !ok {
			return nil, status.Error(codes.NotFound, "reseller not found")
		}
		// Caller cannot update themselves via Management — that's
		// AuthService.ChangePassword's job. Block to avoid privilege
		// loops.
		actor, _ := auth.FromContext(ctx)
		if id == actor.SubjectID {
			return nil, status.Error(codes.PermissionDenied, "use AuthService.ChangePassword for self")
		}
	}
	upd := s.db.Reseller.UpdateOneID(id)
	if v := in.GetName(); v != "" {
		upd.SetName(v)
	}
	if v := in.GetDisplayName(); v != "" {
		upd.SetDisplayName(v)
	}
	if in.GetStatus() != huev1.ResellerStatus_RESELLER_STATUS_UNSPECIFIED {
		upd.SetStatus(resellerStatusToEnt(in.GetStatus()))
	}
	if pw := in.GetPassword(); pw != "" {
		hash, err := auth.HashToken(pw)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "hash password: %v", err)
		}
		upd.SetPasswordHash(hash)
	}
	saved, err := upd.Save(ctx)
	if err != nil {
		return nil, mapEntError(err)
	}
	return resellerToProto(saved), nil
}

func (s *ResellerManagementServer) DeleteReseller(ctx context.Context, req *huev1.DeleteResellerRequest) (*emptypb.Empty, error) {
	id, err := uuid.Parse(req.GetId())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "id: %v", err)
	}
	allowed, err := s.allowedResellerIDs(ctx)
	if err != nil {
		return nil, err
	}
	if allowed != nil {
		if _, ok := allowed[id]; !ok {
			return nil, status.Error(codes.NotFound, "reseller not found")
		}
		actor, _ := auth.FromContext(ctx)
		if id == actor.SubjectID {
			return nil, status.Error(codes.PermissionDenied, "cannot delete self")
		}
	}
	if err := s.db.Reseller.DeleteOneID(id).Exec(ctx); err != nil {
		return nil, mapEntError(err)
	}
	return &emptypb.Empty{}, nil
}
