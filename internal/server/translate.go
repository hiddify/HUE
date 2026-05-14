// Package server holds the gRPC service implementations.
//
// translate.go: proto ↔ ent mapping. Phase-2 naming:
//   * proto Client      ↔ ent.Subscriber
//   * proto Reseller    ↔ ent.Reseller
//   * proto Agent       ↔ ent.Agent
//   * proto Node        ↔ ent.Node (with config map<string, structpb.Value>)
//   * proto UsagePlan   ↔ ent.UsagePlan
//   * proto ApiKey      ↔ ent.ApiKey
//   * proto DomainCertificate ↔ ent.DomainCertificate
package server

import (
	"time"

	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	huev1 "github.com/hiddify/hue/gen/go/hue/v1"
	"github.com/hiddify/hue/internal/ent"
	entagent "github.com/hiddify/hue/internal/ent/agent"
	entapikey "github.com/hiddify/hue/internal/ent/apikey"
	entdomaincertificate "github.com/hiddify/hue/internal/ent/domaincertificate"
	entnode "github.com/hiddify/hue/internal/ent/node"
	entreseller "github.com/hiddify/hue/internal/ent/reseller"
	entsubscriber "github.com/hiddify/hue/internal/ent/subscriber"
	entusageplan "github.com/hiddify/hue/internal/ent/usageplan"
	"github.com/hiddify/hue/internal/eventstore"
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

func clientStatusFromEnt(s entsubscriber.Status) huev1.ClientStatus {
	switch s {
	case entsubscriber.StatusActive:
		return huev1.ClientStatus_CLIENT_STATUS_ACTIVE
	case entsubscriber.StatusSuspended:
		return huev1.ClientStatus_CLIENT_STATUS_SUSPENDED
	case entsubscriber.StatusExpired:
		return huev1.ClientStatus_CLIENT_STATUS_EXPIRED
	case entsubscriber.StatusQuotaUsed:
		return huev1.ClientStatus_CLIENT_STATUS_QUOTA_USED
	case entsubscriber.StatusPenalty:
		return huev1.ClientStatus_CLIENT_STATUS_PENALTY
	case entsubscriber.StatusInactive:
		return huev1.ClientStatus_CLIENT_STATUS_INACTIVE
	}
	return huev1.ClientStatus_CLIENT_STATUS_UNSPECIFIED
}

func clientStatusToEnt(p huev1.ClientStatus) entsubscriber.Status {
	switch p {
	case huev1.ClientStatus_CLIENT_STATUS_ACTIVE:
		return entsubscriber.StatusActive
	case huev1.ClientStatus_CLIENT_STATUS_SUSPENDED:
		return entsubscriber.StatusSuspended
	case huev1.ClientStatus_CLIENT_STATUS_EXPIRED:
		return entsubscriber.StatusExpired
	case huev1.ClientStatus_CLIENT_STATUS_QUOTA_USED:
		return entsubscriber.StatusQuotaUsed
	case huev1.ClientStatus_CLIENT_STATUS_PENALTY:
		return entsubscriber.StatusPenalty
	case huev1.ClientStatus_CLIENT_STATUS_INACTIVE:
		return entsubscriber.StatusInactive
	}
	return entsubscriber.StatusActive
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

func resellerStatusFromEnt(s entreseller.Status) huev1.ResellerStatus {
	if s == entreseller.StatusInactive {
		return huev1.ResellerStatus_RESELLER_STATUS_INACTIVE
	}
	return huev1.ResellerStatus_RESELLER_STATUS_ACTIVE
}

func resellerStatusToEnt(p huev1.ResellerStatus) entreseller.Status {
	if p == huev1.ResellerStatus_RESELLER_STATUS_INACTIVE {
		return entreseller.StatusInactive
	}
	return entreseller.StatusActive
}

func nodeStatusFromEnt(s entnode.Status) huev1.NodeStatus {
	if s == entnode.StatusDisabled {
		return huev1.NodeStatus_NODE_STATUS_DISABLED
	}
	return huev1.NodeStatus_NODE_STATUS_ACTIVE
}

func nodeStatusToEnt(p huev1.NodeStatus) entnode.Status {
	if p == huev1.NodeStatus_NODE_STATUS_DISABLED {
		return entnode.StatusDisabled
	}
	return entnode.StatusActive
}

func agentKindFromEnt(k entagent.Kind) huev1.AgentKind {
	switch k {
	case entagent.KindXray:
		return huev1.AgentKind_AGENT_KIND_XRAY
	case entagent.KindSingbox:
		return huev1.AgentKind_AGENT_KIND_SINGBOX
	case entagent.KindWireguard:
		return huev1.AgentKind_AGENT_KIND_WIREGUARD
	case entagent.KindOpenvpn:
		return huev1.AgentKind_AGENT_KIND_OPENVPN
	case entagent.KindIpsec:
		return huev1.AgentKind_AGENT_KIND_IPSEC
	case entagent.KindRadius:
		return huev1.AgentKind_AGENT_KIND_RADIUS
	case entagent.KindSsh:
		return huev1.AgentKind_AGENT_KIND_SSH
	}
	return huev1.AgentKind_AGENT_KIND_UNSPECIFIED
}

func agentKindToEnt(p huev1.AgentKind) (entagent.Kind, bool) {
	switch p {
	case huev1.AgentKind_AGENT_KIND_XRAY:
		return entagent.KindXray, true
	case huev1.AgentKind_AGENT_KIND_SINGBOX:
		return entagent.KindSingbox, true
	case huev1.AgentKind_AGENT_KIND_WIREGUARD:
		return entagent.KindWireguard, true
	case huev1.AgentKind_AGENT_KIND_OPENVPN:
		return entagent.KindOpenvpn, true
	case huev1.AgentKind_AGENT_KIND_IPSEC:
		return entagent.KindIpsec, true
	case huev1.AgentKind_AGENT_KIND_RADIUS:
		return entagent.KindRadius, true
	case huev1.AgentKind_AGENT_KIND_SSH:
		return entagent.KindSsh, true
	}
	return "", false
}

func apiKeyKindFromEnt(k entapikey.Kind) huev1.ApiKeyKind {
	if k == entapikey.KindOwner {
		return huev1.ApiKeyKind_API_KEY_KIND_OWNER
	}
	return huev1.ApiKeyKind_API_KEY_KIND_AGENT
}

func certIssuerFromEnt(i entdomaincertificate.Issuer) huev1.CertIssuer {
	switch i {
	case entdomaincertificate.IssuerSelfSigned:
		return huev1.CertIssuer_CERT_ISSUER_SELF_SIGNED
	case entdomaincertificate.IssuerAcme:
		return huev1.CertIssuer_CERT_ISSUER_ACME
	case entdomaincertificate.IssuerImported:
		return huev1.CertIssuer_CERT_ISSUER_IMPORTED
	}
	return huev1.CertIssuer_CERT_ISSUER_UNSPECIFIED
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

// ---------- Subscriber (proto Client) ----------

func subscriberToProtoClient(s *ent.Subscriber) *huev1.Client {
	if s == nil {
		return nil
	}
	resellerID := ""
	if s.ResellerID != nil {
		resellerID = s.ResellerID.String()
	}
	out := &huev1.Client{
		Info: &huev1.ClientInfo{
			Id:         s.ID.String(),
			Groups:     s.Groups,
			Status:     clientStatusFromEnt(s.Status),
			ResellerId: resellerID,
			Lifecycle:  lifecycleProto(s.CreatedAt, s.UpdatedAt),
		},
		AuthMethod: &huev1.ClientAuthMethod{
			Username:       s.Username,
			AllowedDevices: s.AllowedDevices,
			// password / private_key never echoed (encrypted at rest;
			// only the active ConfigService.SyncConfig path decrypts).
		},
	}
	if s.FirstConnectionAt != nil {
		out.Info.FirstConnectionAt = tsProto(s.FirstConnectionAt)
	}
	if s.LastConnectionAt != nil {
		out.Info.LastConnectionAt = tsProto(s.LastConnectionAt)
	}
	if plan := s.Edges.ActivePlan; plan != nil {
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
		Id:       p.ID.String(),
		ClientId: p.ClientID.String(),
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
	cfg, _ := mapToStructpb(n.Config)
	return &huev1.Node{
		Id:                  n.ID.String(),
		Name:                n.Name,
		Ips:                 n.Ips,
		AllowedCidrs:        n.AllowedCidrs,
		TrafficMultiplier:   n.TrafficMultiplier,
		ResetPolicy:         &huev1.ResetPolicy{Mode: resetModeFromString(string(n.ResetMode))},
		Current: &huev1.TrafficStats{
			TotalBytes:    n.CurrentTotal,
			UploadBytes:   n.CurrentUpload,
			DownloadBytes: n.CurrentDownload,
		},
		Geo:                 &huev1.Geo{Country: n.Country, City: n.City, Isp: n.Isp, Asn: n.Asn},
		Status:              nodeStatusFromEnt(n.Status),
		BandwidthLimitBytes: n.BandwidthLimitBytes,
		Config:              cfg,
		ServiceHostnames:    n.ServiceHostnames,
		Lifecycle:           lifecycleProto(n.CreatedAt, n.UpdatedAt),
	}
}

// mapToStructpb converts map[string]any → map<string, structpb.Value>.
// Values that don't round-trip cleanly through structpb (e.g. raw
// []byte) are silently skipped — operators store JSON-typed primitives.
func mapToStructpb(m map[string]any) (map[string]*structpb.Value, error) {
	if m == nil {
		return nil, nil
	}
	out := make(map[string]*structpb.Value, len(m))
	for k, v := range m {
		val, err := structpb.NewValue(v)
		if err != nil {
			continue
		}
		out[k] = val
	}
	return out, nil
}

// structpbToMap is the reverse — for CreateNode/UpdateNode requests.
func structpbToMap(m map[string]*structpb.Value) map[string]any {
	if m == nil {
		return nil
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v.AsInterface()
	}
	return out
}

// ---------- Agent ----------

func agentToProto(a *ent.Agent) *huev1.Agent {
	if a == nil {
		return nil
	}
	return &huev1.Agent{
		Id:         a.ID.String(),
		NodeId:     a.NodeID.String(),
		Name:       a.Name,
		Kind:       agentKindFromEnt(a.Kind),
		Version:    a.Version,
		LastSeenAt: tsProto(a.LastSeenAt),
		Current: &huev1.TrafficStats{
			TotalBytes:    a.CurrentTotal,
			UploadBytes:   a.CurrentUpload,
			DownloadBytes: a.CurrentDownload,
		},
		Lifecycle: lifecycleProto(a.CreatedAt, a.UpdatedAt),
	}
}

// ---------- Reseller ----------

func resellerToProto(r *ent.Reseller) *huev1.Reseller {
	if r == nil {
		return nil
	}
	parentID := ""
	if r.ParentID != nil {
		parentID = r.ParentID.String()
	}
	return &huev1.Reseller{
		Id:          r.ID.String(),
		Name:        r.Name,
		DisplayName: r.DisplayName,
		ParentId:    parentID,
		Status:      resellerStatusFromEnt(r.Status),
		// password never echoed back
		Lifecycle: lifecycleProto(r.CreatedAt, r.UpdatedAt),
	}
}

// ---------- ApiKey ----------

func apiKeyToProto(k *ent.ApiKey) *huev1.ApiKey {
	if k == nil {
		return nil
	}
	return &huev1.ApiKey{
		Id:         k.ID.String(),
		Kind:       apiKeyKindFromEnt(k.Kind),
		Name:       k.Name,
		AgentId:    k.AgentID,
		Prefix:     k.Prefix,
		LastUsedAt: tsProto(k.LastUsedAt),
		RevokedAt:  tsProto(k.RevokedAt),
		ExpiresAt:  tsProto(k.ExpiresAt),
		Lifecycle:  lifecycleProto(k.CreatedAt, k.UpdatedAt),
	}
}

// ---------- Events ----------

var eventTypeStringToProto = map[string]huev1.EventType{
	"client_connected":       huev1.EventType_EVENT_TYPE_CLIENT_CONNECTED,
	"client_disconnected":    huev1.EventType_EVENT_TYPE_CLIENT_DISCONNECTED,
	"client_suspended":       huev1.EventType_EVENT_TYPE_CLIENT_SUSPENDED,
	"client_activated":       huev1.EventType_EVENT_TYPE_CLIENT_ACTIVATED,
	"client_limit_reached":   huev1.EventType_EVENT_TYPE_CLIENT_LIMIT_REACHED,
	"usage_recorded":         huev1.EventType_EVENT_TYPE_USAGE_RECORDED,
	"usage_plan_expired":     huev1.EventType_EVENT_TYPE_USAGE_PLAN_EXPIRED,
	"usage_plan_quota_used":  huev1.EventType_EVENT_TYPE_USAGE_PLAN_QUOTA_USED,
	"usage_plan_started":     huev1.EventType_EVENT_TYPE_USAGE_PLAN_STARTED,
	"penalty_applied":        huev1.EventType_EVENT_TYPE_PENALTY_APPLIED,
	"penalty_expired":        huev1.EventType_EVENT_TYPE_PENALTY_EXPIRED,
	"node_reset":             huev1.EventType_EVENT_TYPE_NODE_RESET,
	"node_quota_reached":     huev1.EventType_EVENT_TYPE_NODE_QUOTA_REACHED,
	"reseller_expired":       huev1.EventType_EVENT_TYPE_RESELLER_EXPIRED,
	"reseller_limit_reached": huev1.EventType_EVENT_TYPE_RESELLER_LIMIT_REACHED,
}

var eventTypeProtoToString map[huev1.EventType]string

func init() {
	eventTypeProtoToString = make(map[huev1.EventType]string, len(eventTypeStringToProto))
	for s, p := range eventTypeStringToProto {
		eventTypeProtoToString[p] = s
	}
}

// EventTypeToString converts a proto EventType enum to the canonical string
// used by eventstore. Returns "" for UNSPECIFIED or unknown values.
func EventTypeToString(t huev1.EventType) string {
	return eventTypeProtoToString[t]
}

// eventFromStore maps an in-memory eventstore.Event to the proto wire shape.
func eventFromStore(e eventstore.Event) *huev1.Event {
	out := &huev1.Event{
		Id:         e.ID.String(),
		Type:       eventTypeStringToProto[e.Type],
		ClientId:   e.ClientID,
		PlanId:     e.PlanID,
		NodeId:     e.NodeID,
		AgentId:    e.AgentID,
		ResellerId: e.ResellerID,
		Tags:       e.Tags,
		Timestamp:  timestamppb.New(e.Timestamp),
	}
	if len(e.Metadata) > 0 {
		out.Metadata, _ = mapToStructpb(e.Metadata)
	}
	return out
}

// entEventToProto maps a DB-loaded ent.Event to the proto wire shape.
func entEventToProto(e *ent.Event) *huev1.Event {
	out := &huev1.Event{
		Id:         e.ID.String(),
		Type:       eventTypeStringToProto[string(e.Type)],
		ClientId:   e.ClientID,
		PlanId:     e.PlanID,
		NodeId:     e.NodeID,
		AgentId:    e.AgentID,
		ResellerId: e.ResellerID,
		Tags:       e.Tags,
		Timestamp:  timestamppb.New(e.Ts),
	}
	if len(e.Metadata) > 0 {
		out.Metadata, _ = mapToStructpb(e.Metadata)
	}
	return out
}

// ---------- Cert ----------

func certToProto(c *ent.DomainCertificate, includePrivate bool) *huev1.DomainCertificate {
	if c == nil {
		return nil
	}
	out := &huev1.DomainCertificate{
		Id:              c.ID.String(),
		DomainNames:     c.DomainNames,
		PublicKeyPem:    c.PublicKeyPem,
		ExpiresAt:       timestamppb.New(c.ExpiresAt),
		Issuer:          certIssuerFromEnt(c.Issuer),
		GeneratedNodeId: c.GeneratedNodeID,
		Valid:           c.Valid,
		Lifecycle:       lifecycleProto(c.CreatedAt, c.UpdatedAt),
	}
	if includePrivate {
		// private_key_pem ciphertext is decrypted by the caller before
		// being placed here (Phase 2.5 wires AES-GCM). Phase 2.2 ships
		// the field as base64 ciphertext if not yet decrypted, so
		// nothing accidentally leaks plaintext that wasn't there.
		out.PrivateKeyPem = string(c.PrivateKeyCiphertext)
	}
	return out
}
