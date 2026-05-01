package server

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	huev1 "github.com/hiddify/hue/gen/go/hue/v1"
	"github.com/hiddify/hue/internal/auth"
	"github.com/hiddify/hue/internal/ent"
	entapikey "github.com/hiddify/hue/internal/ent/apikey"
	entmanager "github.com/hiddify/hue/internal/ent/manager"
	entnode "github.com/hiddify/hue/internal/ent/node"
	entuser "github.com/hiddify/hue/internal/ent/user"
	"github.com/hiddify/hue/internal/eventstore"
	"github.com/hiddify/hue/internal/service"
)

// AdminServer implements hue.v1.AdminService. Unimplemented RPCs fall back
// to the embedded UnimplementedAdminServiceServer (returns Unimplemented).
type AdminServer struct {
	huev1.UnimplementedAdminServiceServer

	db     *ent.Client
	engine *service.Engine
	events *eventstore.Store
	logger *slog.Logger
}

// HealthCheck — fast path: a single Ping-equivalent query against ent.
func (s *AdminServer) HealthCheck(ctx context.Context, req *huev1.HealthCheckRequest) (*huev1.HealthCheckResponse, error) {
	if _, err := s.db.User.Query().Limit(1).Count(ctx); err != nil {
		return &huev1.HealthCheckResponse{Status: huev1.HealthStatus_HEALTH_STATUS_NOT_SERVING}, nil
	}
	return &huev1.HealthCheckResponse{Status: huev1.HealthStatus_HEALTH_STATUS_SERVING}, nil
}

// ---------- User ----------

func (s *AdminServer) CreateUser(ctx context.Context, req *huev1.CreateUserRequest) (*huev1.User, error) {
	in := req.GetUser()
	if in == nil || in.GetAuthMethod() == nil {
		return nil, status.Error(codes.InvalidArgument, "user.auth_method.username is required")
	}
	username := in.GetAuthMethod().GetUsername()
	if username == "" {
		return nil, status.Error(codes.InvalidArgument, "username is required")
	}

	create := s.db.User.Create().SetUsername(username)

	if pw := in.GetAuthMethod().GetPassword(); pw != "" {
		hash, err := auth.HashToken(pw)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "hash password: %v", err)
		}
		create.SetPasswordHash(hash)
	}
	if dev := in.GetAuthMethod().GetAllowedDevices(); len(dev) > 0 {
		create.SetAllowedDevices(dev)
	}
	if info := in.GetInfo(); info != nil {
		if g := info.GetGroups(); len(g) > 0 {
			create.SetGroups(g)
		}
		if info.GetStatus() != huev1.UserStatus_USER_STATUS_UNSPECIFIED {
			create.SetStatus(userStatusToEnt(info.GetStatus()))
		}
		if mid := info.GetManagerId(); mid != "" {
			parsed, err := uuid.Parse(mid)
			if err != nil {
				return nil, status.Errorf(codes.InvalidArgument, "manager_id: %v", err)
			}
			create.SetManagerID(parsed)
		}
	}

	saved, err := create.Save(ctx)
	if err != nil {
		return nil, mapEntError(err)
	}
	return userToProto(saved), nil
}

func (s *AdminServer) GetUser(ctx context.Context, req *huev1.GetUserRequest) (*huev1.User, error) {
	id, err := uuid.Parse(req.GetId())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "id: %v", err)
	}
	q := s.db.User.Query().Where(entuser.ID(id))
	if req.GetIncludeAuthMethod() {
		// Currently auth_method is always populated minimally on response.
		// Sensitive fields (password, private_key) are never echoed back.
	}
	q = q.WithActivePlan()
	u, err := q.Only(ctx)
	if err != nil {
		return nil, mapEntError(err)
	}
	return userToProto(u), nil
}

func (s *AdminServer) ListUsers(ctx context.Context, req *huev1.ListUsersRequest) (*huev1.ListUsersResponse, error) {
	pageSize := int(req.GetPageSize())
	if pageSize <= 0 || pageSize > 500 {
		pageSize = 100
	}
	q := s.db.User.Query().WithActivePlan().Limit(pageSize)
	if st := req.GetStatus(); st != huev1.UserStatus_USER_STATUS_UNSPECIFIED {
		q = q.Where(entuser.StatusEQ(userStatusToEnt(st)))
	}
	if mid := req.GetManagerId(); mid != "" {
		if id, err := uuid.Parse(mid); err == nil {
			q = q.Where(entuser.ManagerID(id))
		}
	}
	rows, err := q.All(ctx)
	if err != nil {
		return nil, mapEntError(err)
	}
	out := make([]*huev1.User, 0, len(rows))
	for _, u := range rows {
		out = append(out, userToProto(u))
	}
	return &huev1.ListUsersResponse{Users: out, Total: int32(len(out))}, nil
}

func (s *AdminServer) UpdateUser(ctx context.Context, req *huev1.UpdateUserRequest) (*huev1.User, error) {
	id, err := uuid.Parse(req.GetId())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "id: %v", err)
	}
	in := req.GetUser()
	if in == nil {
		return nil, status.Error(codes.InvalidArgument, "user is required")
	}

	upd := s.db.User.UpdateOneID(id)
	if am := in.GetAuthMethod(); am != nil {
		if u := am.GetUsername(); u != "" {
			upd.SetUsername(u)
		}
		if p := am.GetPassword(); p != "" {
			h, err := auth.HashToken(p)
			if err != nil {
				return nil, status.Errorf(codes.Internal, "hash password: %v", err)
			}
			upd.SetPasswordHash(h)
		}
		if d := am.GetAllowedDevices(); d != nil {
			upd.SetAllowedDevices(d)
		}
	}
	if info := in.GetInfo(); info != nil {
		if info.GetStatus() != huev1.UserStatus_USER_STATUS_UNSPECIFIED {
			upd.SetStatus(userStatusToEnt(info.GetStatus()))
		}
		if g := info.GetGroups(); g != nil {
			upd.SetGroups(g)
		}
	}
	saved, err := upd.Save(ctx)
	if err != nil {
		return nil, mapEntError(err)
	}
	return userToProto(saved), nil
}

func (s *AdminServer) DeleteUser(ctx context.Context, req *huev1.DeleteUserRequest) (*emptypb.Empty, error) {
	id, err := uuid.Parse(req.GetId())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "id: %v", err)
	}
	if err := s.db.User.DeleteOneID(id).Exec(ctx); err != nil {
		return nil, mapEntError(err)
	}
	return &emptypb.Empty{}, nil
}

// ---------- Node ----------

func (s *AdminServer) CreateNode(ctx context.Context, req *huev1.CreateNodeRequest) (*huev1.Node, error) {
	in := req.GetNode()
	if in == nil || in.GetName() == "" {
		return nil, status.Error(codes.InvalidArgument, "node.name is required")
	}
	create := s.db.Node.Create().SetName(in.GetName())
	if ips := in.GetIps(); len(ips) > 0 {
		create.SetIps(ips)
	}
	if cidrs := in.GetAllowedCidrs(); len(cidrs) > 0 {
		create.SetAllowedCidrs(cidrs)
	}
	if m := in.GetTrafficMultiplier(); m > 0 {
		create.SetTrafficMultiplier(m)
	}
	if g := in.GetGeo(); g != nil {
		if g.GetCountry() != "" {
			create.SetCountry(g.GetCountry())
		}
		if g.GetCity() != "" {
			create.SetCity(g.GetCity())
		}
		if g.GetIsp() != "" {
			create.SetIsp(g.GetIsp())
		}
	}
	if in.GetStatus() == huev1.NodeStatus_NODE_STATUS_DISABLED {
		create.SetStatus(entnode.StatusDisabled)
	}
	saved, err := create.Save(ctx)
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
	pageSize := int(req.GetPageSize())
	if pageSize <= 0 || pageSize > 500 {
		pageSize = 100
	}
	q := s.db.Node.Query().Limit(pageSize)
	if st := req.GetStatus(); st == huev1.NodeStatus_NODE_STATUS_DISABLED {
		q = q.Where(entnode.StatusEQ(entnode.StatusDisabled))
	} else if st == huev1.NodeStatus_NODE_STATUS_ACTIVE {
		q = q.Where(entnode.StatusEQ(entnode.StatusActive))
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

// ---------- Manager ----------

func (s *AdminServer) CreateManager(ctx context.Context, req *huev1.CreateManagerRequest) (*huev1.Manager, error) {
	in := req.GetManager()
	if in == nil || in.GetName() == "" {
		return nil, status.Error(codes.InvalidArgument, "manager.name is required")
	}
	create := s.db.Manager.Create().SetName(in.GetName())
	if pid := in.GetParentId(); pid != "" {
		parsed, err := uuid.Parse(pid)
		if err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "parent_id: %v", err)
		}
		create.SetParentID(parsed)
	}
	if in.GetStatus() == huev1.ManagerStatus_MANAGER_STATUS_INACTIVE {
		create.SetStatus(entmanager.StatusInactive)
	}
	saved, err := create.Save(ctx)
	if err != nil {
		return nil, mapEntError(err)
	}
	return managerToProto(saved), nil
}

func (s *AdminServer) GetManager(ctx context.Context, req *huev1.GetManagerRequest) (*huev1.Manager, error) {
	id, err := uuid.Parse(req.GetId())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "id: %v", err)
	}
	m, err := s.db.Manager.Get(ctx, id)
	if err != nil {
		return nil, mapEntError(err)
	}
	return managerToProto(m), nil
}

func (s *AdminServer) ListManagers(ctx context.Context, req *huev1.ListManagersRequest) (*huev1.ListManagersResponse, error) {
	pageSize := int(req.GetPageSize())
	if pageSize <= 0 || pageSize > 500 {
		pageSize = 100
	}
	q := s.db.Manager.Query().Limit(pageSize)
	if pid := req.GetParentId(); pid != "" {
		if id, err := uuid.Parse(pid); err == nil {
			q = q.Where(entmanager.ParentID(id))
		}
	}
	rows, err := q.All(ctx)
	if err != nil {
		return nil, mapEntError(err)
	}
	out := make([]*huev1.Manager, 0, len(rows))
	for _, m := range rows {
		out = append(out, managerToProto(m))
	}
	return &huev1.ListManagersResponse{Managers: out}, nil
}

func (s *AdminServer) DeleteManager(ctx context.Context, req *huev1.DeleteManagerRequest) (*emptypb.Empty, error) {
	id, err := uuid.Parse(req.GetId())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "id: %v", err)
	}
	if err := s.db.Manager.DeleteOneID(id).Exec(ctx); err != nil {
		return nil, mapEntError(err)
	}
	return &emptypb.Empty{}, nil
}

// ---------- ApiKey ----------

func (s *AdminServer) CreateApiKey(ctx context.Context, req *huev1.CreateApiKeyRequest) (*huev1.CreateApiKeyResponse, error) {
	in := req.GetApiKey()
	if in == nil || in.GetName() == "" || in.GetOwnerId() == "" {
		return nil, status.Error(codes.InvalidArgument, "name and owner_id are required")
	}
	var kind auth.ActorKind
	switch in.GetKind() {
	case huev1.ApiKeyKind_API_KEY_KIND_MANAGER:
		kind = auth.KindManager
	case huev1.ApiKeyKind_API_KEY_KIND_SERVICE:
		kind = auth.KindService
	case huev1.ApiKeyKind_API_KEY_KIND_NODE:
		kind = auth.KindNode
	default:
		return nil, status.Error(codes.InvalidArgument, "kind is required")
	}
	prefix, plaintext, hash, err := auth.GenerateKey(kind)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "generate key: %v", err)
	}
	saved, err := s.db.ApiKey.Create().
		SetKind(entApiKeyKind(kind)).
		SetOwnerID(in.GetOwnerId()).
		SetName(in.GetName()).
		SetPrefix(prefix).
		SetHash(hash).
		Save(ctx)
	if err != nil {
		return nil, mapEntError(err)
	}
	return &huev1.CreateApiKeyResponse{
		ApiKey: apiKeyToProto(saved),
		Token:  plaintext,
	}, nil
}

func (s *AdminServer) ListApiKeys(ctx context.Context, req *huev1.ListApiKeysRequest) (*huev1.ListApiKeysResponse, error) {
	pageSize := int(req.GetPageSize())
	if pageSize <= 0 || pageSize > 500 {
		pageSize = 100
	}
	q := s.db.ApiKey.Query().Limit(pageSize)
	if k := req.GetKind(); k != huev1.ApiKeyKind_API_KEY_KIND_UNSPECIFIED {
		switch k {
		case huev1.ApiKeyKind_API_KEY_KIND_MANAGER:
			q = q.Where(entapikey.KindEQ(entapikey.KindManager))
		case huev1.ApiKeyKind_API_KEY_KIND_SERVICE:
			q = q.Where(entapikey.KindEQ(entapikey.KindService))
		case huev1.ApiKeyKind_API_KEY_KIND_NODE:
			q = q.Where(entapikey.KindEQ(entapikey.KindNode))
		}
	}
	if oid := req.GetOwnerId(); oid != "" {
		q = q.Where(entapikey.OwnerID(oid))
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

func (s *AdminServer) RevokeApiKey(ctx context.Context, req *huev1.RevokeApiKeyRequest) (*huev1.ApiKey, error) {
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

// ---------- Helpers ----------

func entApiKeyKind(k auth.ActorKind) entapikey.Kind {
	switch k {
	case auth.KindService:
		return entapikey.KindService
	case auth.KindNode:
		return entapikey.KindNode
	}
	return entapikey.KindManager
}

// mapEntError turns ent's error sentinels into appropriate gRPC codes.
func mapEntError(err error) error {
	switch {
	case err == nil:
		return nil
	case ent.IsNotFound(err):
		return status.Error(codes.NotFound, err.Error())
	case ent.IsConstraintError(err):
		return status.Error(codes.AlreadyExists, err.Error())
	case ent.IsValidationError(err):
		return status.Error(codes.InvalidArgument, err.Error())
	case errors.Is(err, context.Canceled):
		return status.Error(codes.Canceled, err.Error())
	case errors.Is(err, context.DeadlineExceeded):
		return status.Error(codes.DeadlineExceeded, err.Error())
	}
	return status.Error(codes.Internal, err.Error())
}
