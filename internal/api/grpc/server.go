package grpc

import (
	"context"
	"net"

	"github.com/google/uuid"
	"github.com/hiddify/hue-go/internal/domain"
	"github.com/hiddify/hue-go/internal/engine"
	"github.com/hiddify/hue-go/internal/eventstore"
	"github.com/hiddify/hue-go/internal/storage/sqlite"
	pb "github.com/hiddify/hue-go/pkg/proto"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Server implements all gRPC services
type Server struct {
	pb.UnimplementedUsageServiceServer
	pb.UnimplementedAdminServiceServer
	pb.UnimplementedNodeServiceServer

	grpcServer *grpc.Server
	quota      *engine.QuotaEngine
	session    *engine.SessionManager
	penalty    *engine.PenaltyHandler
	geo        *engine.GeoHandler
	events     eventstore.EventStore
	userDB     *sqlite.UserDB
	logger     *zap.Logger
	secret     string
}

// NewServer creates a new gRPC server
func NewServer(
	quota *engine.QuotaEngine,
	session *engine.SessionManager,
	penalty *engine.PenaltyHandler,
	geo *engine.GeoHandler,
	events eventstore.EventStore,
	logger *zap.Logger,
	secret string,
) *Server {
	return &Server{
		quota:   quota,
		session: session,
		penalty: penalty,
		geo:     geo,
		events:  events,
		logger:  logger,
		secret:  secret,
	}
}

// SetUserDB sets the user database for admin operations
func (s *Server) SetUserDB(db *sqlite.UserDB) {
	s.userDB = db
}

// UsageService implementation

func (s *Server) ReportUsage(ctx context.Context, req *pb.ReportUsageRequest) (*pb.ReportUsageResponse, error) {
	report := s.protoToDomainUsageReport(req.Report)

	// Process usage report through quota engine
	quotaResult, err := s.quota.CheckQuota(report.UserID, report.Upload, report.Download)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "quota check failed: %v", err)
	}

	result := &domain.UsageReportResult{
		UserID:           report.UserID,
		Accepted:         false,
		QuotaExceeded:    false,
		SessionLimitHit:  false,
		PenaltyApplied:   false,
		ShouldDisconnect: false,
	}

	// Check penalty
	penaltyResult := s.penalty.CheckPenalty(report.UserID)
	if penaltyResult.HasPenalty {
		result.ShouldDisconnect = true
		result.Reason = "user has active penalty"
		return &pb.ReportUsageResponse{Result: s.domainToProtoResult(result)}, nil
	}

	// Check session
	if quotaResult.Pkg != nil {
		sessionResult := s.session.CheckSession(report.UserID, report.SessionID, report.ClientIP, quotaResult.Pkg.MaxConcurrent)
		if sessionResult.SessionLimitHit {
			s.penalty.ApplyPenalty(report.UserID, "concurrent_session_limit_exceeded")
			result.PenaltyApplied = true
			result.ShouldDisconnect = true
			result.Reason = "concurrent session limit exceeded"
			return &pb.ReportUsageResponse{Result: s.domainToProtoResult(result)}, nil
		}
	}

	// Check quota
	if !quotaResult.CanUse {
		result.QuotaExceeded = quotaResult.QuotaExceeded
		result.ShouldDisconnect = true
		result.Reason = quotaResult.Reason
		return &pb.ReportUsageResponse{Result: s.domainToProtoResult(result)}, nil
	}

	// Extract geo data
	var geoData *domain.GeoData
	if s.geo != nil && s.geo.IsReady() && report.ClientIP != "" {
		geoData = s.geo.ExtractGeo(report.ClientIP)
	}

	// Add session
	s.session.AddSession(report.UserID, report.SessionID, report.ClientIP, geoData)

	// Record usage
	if err := s.quota.RecordUsage(report.UserID, report.Upload, report.Download); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to record usage: %v", err)
	}

	// Update node and service usage
	if report.NodeID != "" {
		s.userDB.UpdateNodeUsage(report.NodeID, report.Upload, report.Download)
	}
	if report.ServiceID != "" {
		s.userDB.UpdateServiceUsage(report.ServiceID, report.Upload, report.Download)
	}

	result.Accepted = true
	if quotaResult.Pkg != nil {
		result.PackageID = quotaResult.Pkg.ID
	}

	s.logger.Debug("usage reported",
		zap.String("user_id", report.UserID),
		zap.Int64("upload", report.Upload),
		zap.Int64("download", report.Download),
		zap.Bool("accepted", result.Accepted),
	)

	return &pb.ReportUsageResponse{Result: s.domainToProtoResult(result)}, nil
}

func (s *Server) BatchReportUsage(ctx context.Context, req *pb.BatchReportUsageRequest) (*pb.BatchReportUsageResponse, error) {
	results := make([]*pb.UsageReportResult, len(req.Reports))

	for i, report := range req.Reports {
		resp, err := s.ReportUsage(ctx, &pb.ReportUsageRequest{Report: report})
		if err != nil {
			results[i] = &pb.UsageReportResult{
				UserId:   report.UserId,
				Accepted: false,
				Reason:   err.Error(),
			}
		} else {
			results[i] = resp.Result
		}
	}

	return &pb.BatchReportUsageResponse{Results: results}, nil
}

func (s *Server) GetDisconnectCommands(ctx context.Context, req *pb.GetDisconnectCommandsRequest) (*pb.GetDisconnectCommandsResponse, error) {
	// Get disconnect batch from cache
	sessionCache := s.session
	_ = sessionCache // We'll use the penalty handler's disconnect queue

	// For now, return empty - this would be implemented with a proper disconnect queue
	return &pb.GetDisconnectCommandsResponse{Commands: []*pb.DisconnectCommand{}}, nil
}

// AdminService implementation - User operations

func (s *Server) CreateUser(ctx context.Context, req *pb.CreateUserRequest) (*pb.User, error) {
	user := &domain.User{
		ID:              uuid.New().String(),
		Username:        req.Username,
		Password:        req.Password,
		PublicKey:       req.PublicKey,
		PrivateKey:      req.PrivateKey,
		CACertList:      req.CaCertList,
		Groups:          req.Groups,
		AllowedDevices:  req.AllowedDevices,
		Status:          domain.UserStatusActive,
		ActivePackageID: nil,
	}

	if req.ActivePackageId != "" {
		user.ActivePackageID = &req.ActivePackageId
	}

	if err := s.userDB.CreateUser(user); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to create user: %v", err)
	}

	return s.domainToProtoUser(user), nil
}

func (s *Server) GetUser(ctx context.Context, req *pb.GetUserRequest) (*pb.User, error) {
	user, err := s.userDB.GetUser(req.Id)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to get user: %v", err)
	}
	if user == nil {
		return nil, status.Errorf(codes.NotFound, "user not found")
	}

	return s.domainToProtoUser(user), nil
}

func (s *Server) ListUsers(ctx context.Context, req *pb.ListUsersRequest) (*pb.ListUsersResponse, error) {
	filter := &domain.UserFilter{
		Limit:  int(req.Limit),
		Offset: int(req.Offset),
	}

	if req.Status != "" {
		status := domain.UserStatus(req.Status)
		filter.Status = &status
	}
	if req.Group != "" {
		filter.Group = &req.Group
	}
	if req.Search != "" {
		filter.Search = &req.Search
	}

	users, err := s.userDB.ListUsers(filter)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to list users: %v", err)
	}

	protoUsers := make([]*pb.User, len(users))
	for i, u := range users {
		protoUsers[i] = s.domainToProtoUser(u)
	}

	return &pb.ListUsersResponse{
		Users: protoUsers,
		Total: int32(len(protoUsers)),
	}, nil
}

func (s *Server) UpdateUser(ctx context.Context, req *pb.UpdateUserRequest) (*pb.User, error) {
	user, err := s.userDB.GetUser(req.Id)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to get user: %v", err)
	}
	if user == nil {
		return nil, status.Errorf(codes.NotFound, "user not found")
	}

	// Update fields
	if req.Username != "" {
		user.Username = req.Username
	}
	if req.Password != "" {
		user.Password = req.Password
	}
	if req.PublicKey != "" {
		user.PublicKey = req.PublicKey
	}
	if req.PrivateKey != "" {
		user.PrivateKey = req.PrivateKey
	}
	if len(req.CaCertList) > 0 {
		user.CACertList = req.CaCertList
	}
	if len(req.Groups) > 0 {
		user.Groups = req.Groups
	}
	if len(req.AllowedDevices) > 0 {
		user.AllowedDevices = req.AllowedDevices
	}
	if req.Status != "" {
		user.Status = domain.UserStatus(req.Status)
	}
	if req.ActivePackageId != "" {
		user.ActivePackageID = &req.ActivePackageId
	}

	if err := s.userDB.UpdateUser(user); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to update user: %v", err)
	}

	return s.domainToProtoUser(user), nil
}

func (s *Server) DeleteUser(ctx context.Context, req *pb.DeleteUserRequest) (*pb.Empty, error) {
	if err := s.userDB.DeleteUser(req.Id); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to delete user: %v", err)
	}
	return &pb.Empty{}, nil
}

// AdminService implementation - Package operations

func (s *Server) CreatePackage(ctx context.Context, req *pb.CreatePackageRequest) (*pb.Package, error) {
	pkg := &domain.Package{
		ID:            uuid.New().String(),
		UserID:        req.UserId,
		TotalTraffic:  req.TotalTraffic,
		UploadLimit:   req.UploadLimit,
		DownloadLimit: req.DownloadLimit,
		ResetMode:     domain.ResetMode(req.ResetMode),
		Duration:      req.Duration,
		MaxConcurrent: int(req.MaxConcurrent),
		Status:        domain.PackageStatusActive,
	}

	if req.StartAt > 0 {
		t := domain.ParseTime(req.StartAt)
		pkg.StartAt = &t
	}

	if err := s.userDB.CreatePackage(pkg); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to create package: %v", err)
	}

	return s.domainToProtoPackage(pkg), nil
}

func (s *Server) GetPackage(ctx context.Context, req *pb.GetPackageRequest) (*pb.Package, error) {
	pkg, err := s.userDB.GetPackage(req.Id)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to get package: %v", err)
	}
	if pkg == nil {
		return nil, status.Errorf(codes.NotFound, "package not found")
	}

	return s.domainToProtoPackage(pkg), nil
}

func (s *Server) GetPackageByUser(ctx context.Context, req *pb.GetPackageByUserRequest) (*pb.Package, error) {
	pkg, err := s.userDB.GetPackageByUserID(req.UserId)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to get package: %v", err)
	}
	if pkg == nil {
		return nil, status.Errorf(codes.NotFound, "package not found")
	}

	return s.domainToProtoPackage(pkg), nil
}

func (s *Server) DeletePackage(ctx context.Context, req *pb.DeletePackageRequest) (*pb.Empty, error) {
	// Not implemented - packages are deleted via user cascade
	return &pb.Empty{}, nil
}

// AdminService implementation - Node operations

func (s *Server) CreateNode(ctx context.Context, req *pb.CreateNodeRequest) (*pb.Node, error) {
	node := &domain.Node{
		ID:                uuid.New().String(),
		SecretKey:         req.SecretKey,
		Name:              req.Name,
		AllowedIPs:        req.AllowedIps,
		TrafficMultiplier: req.TrafficMultiplier,
		ResetMode:         domain.ResetMode(req.ResetMode),
		ResetDay:          int(req.ResetDay),
		Country:           req.Country,
		City:              req.City,
		ISP:               req.Isp,
	}

	if err := s.userDB.CreateNode(node); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to create node: %v", err)
	}

	return s.domainToProtoNode(node), nil
}

func (s *Server) GetNode(ctx context.Context, req *pb.GetNodeRequest) (*pb.Node, error) {
	node, err := s.userDB.GetNode(req.Id)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to get node: %v", err)
	}
	if node == nil {
		return nil, status.Errorf(codes.NotFound, "node not found")
	}

	return s.domainToProtoNode(node), nil
}

func (s *Server) ListNodes(ctx context.Context, req *pb.Empty) (*pb.ListNodesResponse, error) {
	nodes, err := s.userDB.ListNodes()
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to list nodes: %v", err)
	}

	protoNodes := make([]*pb.Node, len(nodes))
	for i, n := range nodes {
		protoNodes[i] = s.domainToProtoNode(n)
	}

	return &pb.ListNodesResponse{Nodes: protoNodes}, nil
}

func (s *Server) DeleteNode(ctx context.Context, req *pb.DeleteNodeRequest) (*pb.Empty, error) {
	if err := s.userDB.DeleteNode(req.Id); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to delete node: %v", err)
	}
	return &pb.Empty{}, nil
}

// AdminService implementation - Service operations

func (s *Server) CreateService(ctx context.Context, req *pb.CreateServiceRequest) (*pb.Service, error) {
	authMethods := make([]domain.AuthMethod, len(req.AllowedAuthMethods))
	for i, m := range req.AllowedAuthMethods {
		authMethods[i] = domain.AuthMethod(m)
	}

	service := &domain.Service{
		ID:                 uuid.New().String(),
		SecretKey:          req.SecretKey,
		NodeID:             req.NodeId,
		Name:               req.Name,
		Protocol:           req.Protocol,
		AllowedAuthMethods: authMethods,
		CallbackURL:        req.CallbackUrl,
	}

	if err := s.userDB.CreateService(service); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to create service: %v", err)
	}

	return s.domainToProtoService(service), nil
}

func (s *Server) GetService(ctx context.Context, req *pb.GetServiceRequest) (*pb.Service, error) {
	service, err := s.userDB.GetService(req.Id)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to get service: %v", err)
	}
	if service == nil {
		return nil, status.Errorf(codes.NotFound, "service not found")
	}

	return s.domainToProtoService(service), nil
}

func (s *Server) DeleteService(ctx context.Context, req *pb.DeleteServiceRequest) (*pb.Empty, error) {
	if err := s.userDB.DeleteService(req.Id); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to delete service: %v", err)
	}
	return &pb.Empty{}, nil
}

// AdminService implementation - Event operations

func (s *Server) GetEvents(ctx context.Context, req *pb.GetEventsRequest) (*pb.GetEventsResponse, error) {
	var eventType *domain.EventType
	if req.Type != "" {
		t := domain.EventType(req.Type)
		eventType = &t
	}

	var userID *string
	if req.UserId != "" {
		userID = &req.UserId
	}

	events, err := s.events.GetEvents(eventType, userID, int(req.Limit))
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to get events: %v", err)
	}

	protoEvents := make([]*pb.Event, len(events))
	for i, e := range events {
		protoEvents[i] = s.domainToProtoEvent(e)
	}

	return &pb.GetEventsResponse{Events: protoEvents}, nil
}

// NodeService implementation

func (s *Server) Authenticate(ctx context.Context, req *pb.AuthenticateRequest) (*pb.AuthenticateResponse, error) {
	node, err := s.userDB.GetNodeBySecretKey(req.SecretKey)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "authentication failed: %v", err)
	}
	if node == nil {
		return &pb.AuthenticateResponse{
			Success: false,
			Error:   "invalid secret key",
		}, nil
	}

	return &pb.AuthenticateResponse{
		Success: true,
		NodeId:  node.ID,
	}, nil
}

func (s *Server) Heartbeat(ctx context.Context, req *pb.HeartbeatRequest) (*pb.HeartbeatResponse, error) {
	// Update node stats
	if req.NodeId != "" {
		// Node heartbeat - could update last_seen timestamp
		s.logger.Debug("node heartbeat", zap.String("node_id", req.NodeId))
	}

	return &pb.HeartbeatResponse{Acknowledged: true}, nil
}

// Conversion helpers

func (s *Server) protoToDomainUsageReport(pb *pb.UsageReport) *domain.UsageReport {
	return &domain.UsageReport{
		ID:        pb.Id,
		UserID:    pb.UserId,
		NodeID:    pb.NodeId,
		ServiceID: pb.ServiceId,
		Upload:    pb.Upload,
		Download:  pb.Download,
		SessionID: pb.SessionId,
		ClientIP:  pb.ClientIp,
		Tags:      pb.Tags,
		Timestamp: domain.ParseTime(pb.Timestamp),
	}
}

func (s *Server) domainToProtoResult(r *domain.UsageReportResult) *pb.UsageReportResult {
	return &pb.UsageReportResult{
		UserId:           r.UserID,
		PackageId:        r.PackageID,
		Accepted:         r.Accepted,
		QuotaExceeded:    r.QuotaExceeded,
		SessionLimitHit:  r.SessionLimitHit,
		PenaltyApplied:   r.PenaltyApplied,
		ShouldDisconnect: r.ShouldDisconnect,
		Reason:           r.Reason,
	}
}

func (s *Server) domainToProtoUser(u *domain.User) *pb.User {
	var firstConn, lastConn int64
	if u.FirstConnectionAt != nil {
		firstConn = u.FirstConnectionAt.Unix()
	}
	if u.LastConnectionAt != nil {
		lastConn = u.LastConnectionAt.Unix()
	}

	var activePkgID string
	if u.ActivePackageID != nil {
		activePkgID = *u.ActivePackageID
	}

	return &pb.User{
		Id:                u.ID,
		Username:          u.Username,
		PublicKey:         u.PublicKey,
		CaCertList:        u.CACertList,
		Groups:            u.Groups,
		AllowedDevices:    u.AllowedDevices,
		Status:            string(u.Status),
		ActivePackageId:   activePkgID,
		FirstConnectionAt: firstConn,
		LastConnectionAt:  lastConn,
		CreatedAt:         u.CreatedAt.Unix(),
		UpdatedAt:         u.UpdatedAt.Unix(),
	}
}

func (s *Server) domainToProtoPackage(p *domain.Package) *pb.Package {
	var startAt, expiresAt int64
	if p.StartAt != nil {
		startAt = p.StartAt.Unix()
	}
	if p.ExpiresAt != nil {
		expiresAt = p.ExpiresAt.Unix()
	}

	return &pb.Package{
		Id:              p.ID,
		UserId:          p.UserID,
		TotalTraffic:    p.TotalTraffic,
		UploadLimit:     p.UploadLimit,
		DownloadLimit:   p.DownloadLimit,
		ResetMode:       string(p.ResetMode),
		Duration:        p.Duration,
		StartAt:         startAt,
		MaxConcurrent:   int32(p.MaxConcurrent),
		Status:          string(p.Status),
		CurrentUpload:   p.CurrentUpload,
		CurrentDownload: p.CurrentDownload,
		CurrentTotal:    p.CurrentTotal,
		ExpiresAt:       expiresAt,
		CreatedAt:       p.CreatedAt.Unix(),
		UpdatedAt:       p.UpdatedAt.Unix(),
	}
}

func (s *Server) domainToProtoNode(n *domain.Node) *pb.Node {
	return &pb.Node{
		Id:                n.ID,
		Name:              n.Name,
		AllowedIps:        n.AllowedIPs,
		TrafficMultiplier: n.TrafficMultiplier,
		ResetMode:         string(n.ResetMode),
		ResetDay:          int32(n.ResetDay),
		CurrentUpload:     n.CurrentUpload,
		CurrentDownload:   n.CurrentDownload,
		Country:           n.Country,
		City:              n.City,
		Isp:               n.ISP,
		CreatedAt:         n.CreatedAt.Unix(),
		UpdatedAt:         n.UpdatedAt.Unix(),
	}
}

func (srv *Server) domainToProtoService(svc *domain.Service) *pb.Service {
	authMethods := make([]string, len(svc.AllowedAuthMethods))
	for i, m := range svc.AllowedAuthMethods {
		authMethods[i] = string(m)
	}

	return &pb.Service{
		Id:                 svc.ID,
		NodeId:             svc.NodeID,
		Name:               svc.Name,
		Protocol:           svc.Protocol,
		AllowedAuthMethods: authMethods,
		CallbackUrl:        svc.CallbackURL,
		CurrentUpload:      svc.CurrentUpload,
		CurrentDownload:    svc.CurrentDownload,
		CreatedAt:          svc.CreatedAt.Unix(),
		UpdatedAt:          svc.UpdatedAt.Unix(),
	}
}

func (s *Server) domainToProtoEvent(e *domain.Event) *pb.Event {
	var userID, packageID, nodeID, serviceID string
	if e.UserID != nil {
		userID = *e.UserID
	}
	if e.PackageID != nil {
		packageID = *e.PackageID
	}
	if e.NodeID != nil {
		nodeID = *e.NodeID
	}
	if e.ServiceID != nil {
		serviceID = *e.ServiceID
	}

	return &pb.Event{
		Id:        e.ID,
		Type:      string(e.Type),
		UserId:    userID,
		PackageId: packageID,
		NodeId:    nodeID,
		ServiceId: serviceID,
		Tags:      e.Tags,
		Metadata:  e.Metadata,
		Timestamp: e.Timestamp.Unix(),
	}
}

// GracefulStop gracefully stops the server
func (srv *Server) GracefulStop() {
	if srv.grpcServer != nil {
		srv.grpcServer.GracefulStop()
	}
}

// Serve starts the gRPC server on the given listener
func (srv *Server) Serve(lis net.Listener) error {
	// Create the gRPC server
	srv.grpcServer = grpc.NewServer()

	// Register all services
	pb.RegisterUsageServiceServer(srv.grpcServer, srv)
	pb.RegisterAdminServiceServer(srv.grpcServer, srv)
	pb.RegisterNodeServiceServer(srv.grpcServer, srv)

	return srv.grpcServer.Serve(lis)
}
