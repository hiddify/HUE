// Package server holds the gRPC service implementations. Each method is a
// thin shim that translates proto messages to service-layer inputs and the
// service-layer outputs back to proto. The gRPC-gateway-generated HTTP
// handlers reach the same Go functions via an in-process loopback (set up
// in cmd/hue/main.go), so REST and gRPC share auth, validation, and engine
// code with no duplication.
package server

import (
	"time"

	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	huev1 "github.com/hiddify/hue/gen/go/hue/v1"
	"github.com/hiddify/hue/internal/ent"
	entapikey "github.com/hiddify/hue/internal/ent/apikey"
	entmanager "github.com/hiddify/hue/internal/ent/manager"
	entmplan "github.com/hiddify/hue/internal/ent/managerplan"
	entnode "github.com/hiddify/hue/internal/ent/node"
	entusageplan "github.com/hiddify/hue/internal/ent/usageplan"
	entuser "github.com/hiddify/hue/internal/ent/user"
)

// ---------- Time helpers ----------

func tsProto(t *time.Time) *timestamppb.Timestamp {
	if t == nil {
		return nil
	}
	return timestamppb.New(*t)
}

func tsEnt(p *timestamppb.Timestamp) *time.Time {
	if p == nil || !p.IsValid() {
		return nil
	}
	t := p.AsTime()
	return &t
}

func durSeconds(d *durationpb.Duration) int64 {
	if d == nil {
		return 0
	}
	return int64(d.AsDuration().Seconds())
}

func secondsDur(s int64) *durationpb.Duration {
	if s == 0 {
		return nil
	}
	return durationpb.New(time.Duration(s) * time.Second)
}

func lifecycleProto(createdAt, updatedAt time.Time) *huev1.Lifecycle {
	return &huev1.Lifecycle{
		CreatedAt: timestamppb.New(createdAt),
		UpdatedAt: timestamppb.New(updatedAt),
	}
}

// ---------- Enum translation ----------

func userStatusFromEnt(s entuser.Status) huev1.UserStatus {
	switch s {
	case entuser.StatusActive:
		return huev1.UserStatus_USER_STATUS_ACTIVE
	case entuser.StatusSuspended:
		return huev1.UserStatus_USER_STATUS_SUSPENDED
	case entuser.StatusExpired:
		return huev1.UserStatus_USER_STATUS_EXPIRED
	case entuser.StatusQuotaUsed:
		return huev1.UserStatus_USER_STATUS_QUOTA_USED
	case entuser.StatusPenalty:
		return huev1.UserStatus_USER_STATUS_PENALTY
	case entuser.StatusInactive:
		return huev1.UserStatus_USER_STATUS_INACTIVE
	}
	return huev1.UserStatus_USER_STATUS_UNSPECIFIED
}

func userStatusToEnt(p huev1.UserStatus) entuser.Status {
	switch p {
	case huev1.UserStatus_USER_STATUS_ACTIVE:
		return entuser.StatusActive
	case huev1.UserStatus_USER_STATUS_SUSPENDED:
		return entuser.StatusSuspended
	case huev1.UserStatus_USER_STATUS_EXPIRED:
		return entuser.StatusExpired
	case huev1.UserStatus_USER_STATUS_QUOTA_USED:
		return entuser.StatusQuotaUsed
	case huev1.UserStatus_USER_STATUS_PENALTY:
		return entuser.StatusPenalty
	case huev1.UserStatus_USER_STATUS_INACTIVE:
		return entuser.StatusInactive
	}
	return entuser.StatusActive
}

func planStatusFromEnt(s entusageplan.Status) huev1.UsagePlanStatus {
	switch s {
	case entusageplan.StatusActive:
		return huev1.UsagePlanStatus_USAGE_PLAN_STATUS_ACTIVE
	case entusageplan.StatusExpired:
		return huev1.UsagePlanStatus_USAGE_PLAN_STATUS_EXPIRED
	case entusageplan.StatusQuotaUsed:
		return huev1.UsagePlanStatus_USAGE_PLAN_STATUS_QUOTA_USED
	case entusageplan.StatusSuspended:
		return huev1.UsagePlanStatus_USAGE_PLAN_STATUS_SUSPENDED
	case entusageplan.StatusSessionReached:
		return huev1.UsagePlanStatus_USAGE_PLAN_STATUS_SESSION_REACHED
	case entusageplan.StatusPenalty:
		return huev1.UsagePlanStatus_USAGE_PLAN_STATUS_PENALTY
	case entusageplan.StatusInactive:
		return huev1.UsagePlanStatus_USAGE_PLAN_STATUS_INACTIVE
	}
	return huev1.UsagePlanStatus_USAGE_PLAN_STATUS_UNSPECIFIED
}

func resetModeFromString(s string) huev1.ResetMode {
	switch s {
	case "no_reset":
		return huev1.ResetMode_RESET_MODE_NO_RESET
	case "hourly":
		return huev1.ResetMode_RESET_MODE_HOURLY
	case "daily":
		return huev1.ResetMode_RESET_MODE_DAILY
	case "weekly":
		return huev1.ResetMode_RESET_MODE_WEEKLY
	case "monthly":
		return huev1.ResetMode_RESET_MODE_MONTHLY
	case "yearly":
		return huev1.ResetMode_RESET_MODE_YEARLY
	}
	return huev1.ResetMode_RESET_MODE_UNSPECIFIED
}

func resetModeToString(p huev1.ResetMode) string {
	switch p {
	case huev1.ResetMode_RESET_MODE_HOURLY:
		return "hourly"
	case huev1.ResetMode_RESET_MODE_DAILY:
		return "daily"
	case huev1.ResetMode_RESET_MODE_WEEKLY:
		return "weekly"
	case huev1.ResetMode_RESET_MODE_MONTHLY:
		return "monthly"
	case huev1.ResetMode_RESET_MODE_YEARLY:
		return "yearly"
	default:
		return "no_reset"
	}
}

// ---------- User ----------

func userToProto(u *ent.User) *huev1.User {
	if u == nil {
		return nil
	}
	managerID := ""
	if u.ManagerID != nil {
		managerID = u.ManagerID.String()
	}
	out := &huev1.User{
		Info: &huev1.UserInfo{
			Id:        u.ID.String(),
			Groups:    u.Groups,
			Status:    userStatusFromEnt(u.Status),
			ManagerId: managerID,
			Lifecycle: lifecycleProto(u.CreatedAt, u.UpdatedAt),
		},
		AuthMethod: &huev1.UserAuthMethod{
			Username:       u.Username,
			AllowedDevices: u.AllowedDevices,
			// password / private_key intentionally NOT echoed back; they
			// are sensitive and the ent fields are write-only / hashed.
		},
	}
	if plan := u.Edges.ActivePlan; plan != nil {
		out.UsagePlan = usagePlanToProto(plan)
	}
	return out
}

// ---------- UsagePlan ----------

func usagePlanToProto(p *ent.UsagePlan) *huev1.UsagePlan {
	if p == nil {
		return nil
	}
	return &huev1.UsagePlan{
		Id:     p.ID.String(),
		UserId: p.UserID.String(),
		Limit: &huev1.TrafficStats{
			TotalBytes:    p.TotalLimit,
			UploadBytes:   p.UploadLimit,
			DownloadBytes: p.DownloadLimit,
		},
		Current: &huev1.TrafficStats{
			TotalBytes:    p.CurrentTotal,
			UploadBytes:   p.CurrentUpload,
			DownloadBytes: p.CurrentDownload,
		},
		Status:        planStatusFromEnt(p.Status),
		ResetPolicy:   &huev1.ResetPolicy{Mode: resetModeFromString(string(p.ResetMode))},
		StartAt:       tsProto(p.StartAt),
		Duration:      secondsDur(p.DurationSeconds),
		MaxConcurrent: p.MaxConcurrent,
		ExpiresAt:     tsProto(p.ExpiresAt),
		Lifecycle:     lifecycleProto(p.CreatedAt, p.UpdatedAt),
	}
}

// ---------- Node ----------

func nodeToProto(n *ent.Node) *huev1.Node {
	if n == nil {
		return nil
	}
	statusOut := huev1.NodeStatus_NODE_STATUS_ACTIVE
	if n.Status == entnode.StatusDisabled {
		statusOut = huev1.NodeStatus_NODE_STATUS_DISABLED
	}
	return &huev1.Node{
		Id:                n.ID.String(),
		Name:              n.Name,
		Ips:               n.Ips,
		AllowedCidrs:      n.AllowedCidrs,
		TrafficMultiplier: n.TrafficMultiplier,
		ResetPolicy:       &huev1.ResetPolicy{Mode: resetModeFromString(string(n.ResetMode))},
		Current: &huev1.TrafficStats{
			TotalBytes:    n.CurrentTotal,
			UploadBytes:   n.CurrentUpload,
			DownloadBytes: n.CurrentDownload,
		},
		Geo:       &huev1.Geo{Country: n.Country, City: n.City, Isp: n.Isp, Asn: n.Asn},
		Status:    statusOut,
		Lifecycle: lifecycleProto(n.CreatedAt, n.UpdatedAt),
	}
}

// ---------- Manager ----------

func managerToProto(m *ent.Manager) *huev1.Manager {
	if m == nil {
		return nil
	}
	parentID := ""
	if m.ParentID != nil {
		parentID = m.ParentID.String()
	}
	statusOut := huev1.ManagerStatus_MANAGER_STATUS_ACTIVE
	if m.Status == entmanager.StatusInactive {
		statusOut = huev1.ManagerStatus_MANAGER_STATUS_INACTIVE
	}
	return &huev1.Manager{
		Id:        m.ID.String(),
		Name:      m.Name,
		ParentId:  parentID,
		Status:    statusOut,
		Lifecycle: lifecycleProto(m.CreatedAt, m.UpdatedAt),
	}
}

func managerPlanToProto(p *ent.ManagerPlan) *huev1.ManagerPlan {
	if p == nil {
		return nil
	}
	statusOut := huev1.ManagerStatus_MANAGER_STATUS_ACTIVE
	if p.Status == entmplan.StatusInactive {
		statusOut = huev1.ManagerStatus_MANAGER_STATUS_INACTIVE
	}
	_ = statusOut // ManagerPlan in proto doesn't have its own status enum repeat
	return &huev1.ManagerPlan{
		Id:        p.ID.String(),
		ManagerId: p.ManagerID.String(),
		Limit: &huev1.TrafficStats{
			TotalBytes:    p.TotalLimit,
			UploadBytes:   p.UploadLimit,
			DownloadBytes: p.DownloadLimit,
		},
		Current: &huev1.TrafficStats{
			TotalBytes:    p.CurrentTotal,
			UploadBytes:   p.CurrentUpload,
			DownloadBytes: p.CurrentDownload,
			Sessions:      int64(p.CurrentSessions),
			OnlineUsers:   int64(p.CurrentOnlineUsers),
			ActiveUsers:   int64(p.CurrentActiveUsers),
		},
		MaxSessions:    p.MaxSessions,
		MaxOnlineUsers: p.MaxOnlineUsers,
		MaxActiveUsers: p.MaxActiveUsers,
		ResetPolicy:    &huev1.ResetPolicy{Mode: resetModeFromString(string(p.ResetMode))},
		StartAt:        tsProto(p.StartAt),
		Duration:       secondsDur(p.DurationSeconds),
		ExpiresAt:      tsProto(p.ExpiresAt),
		Lifecycle:      lifecycleProto(p.CreatedAt, p.UpdatedAt),
	}
}

// ---------- ApiKey ----------

func apiKeyToProto(k *ent.ApiKey) *huev1.ApiKey {
	if k == nil {
		return nil
	}
	kind := huev1.ApiKeyKind_API_KEY_KIND_UNSPECIFIED
	switch k.Kind {
	case entapikey.KindManager:
		kind = huev1.ApiKeyKind_API_KEY_KIND_MANAGER
	case entapikey.KindService:
		kind = huev1.ApiKeyKind_API_KEY_KIND_SERVICE
	case entapikey.KindNode:
		kind = huev1.ApiKeyKind_API_KEY_KIND_NODE
	}
	return &huev1.ApiKey{
		Id:         k.ID.String(),
		Kind:       kind,
		OwnerId:    k.OwnerID,
		Name:       k.Name,
		Prefix:     k.Prefix,
		LastUsedAt: tsProto(k.LastUsedAt),
		RevokedAt:  tsProto(k.RevokedAt),
		Lifecycle:  lifecycleProto(k.CreatedAt, k.UpdatedAt),
	}
}
