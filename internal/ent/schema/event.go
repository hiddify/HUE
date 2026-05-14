package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// Event is the immutable audit log row. PARTITIONED BY RANGE (ts) at
// the migration level. Phase-2 vocabulary: client_id (was user_id),
// agent_id (was service_id), reseller_id (was manager_id).
type Event struct{ ent.Schema }

func (Event) Mixin() []ent.Mixin { return []ent.Mixin{UUIDMixin{}} }

func (Event) Fields() []ent.Field {
	return []ent.Field{
		field.Enum("type").
			NamedValues(
				"ClientConnected", "client_connected",
				"ClientDisconnected", "client_disconnected",
				"ClientSuspended", "client_suspended",
				"ClientActivated", "client_activated",
				"ClientLimitReached", "client_limit_reached",
				"UsageRecorded", "usage_recorded",
				"UsagePlanExpired", "usage_plan_expired",
				"UsagePlanQuotaUsed", "usage_plan_quota_used",
				"UsagePlanStarted", "usage_plan_started",
				"PenaltyApplied", "penalty_applied",
				"PenaltyExpired", "penalty_expired",
				"NodeReset", "node_reset",
				"NodeQuotaReached", "node_quota_reached",
				"ResellerExpired", "reseller_expired",
				"ResellerLimitReached", "reseller_limit_reached",
				"ResellerPlanStarted", "reseller_plan_started",
				"LoginSucceeded", "login_succeeded",
				"LoginFailed", "login_failed",
				"LoginLockedOut", "login_locked_out",
				"OwnerSudoLogin", "owner_sudo_login",
				"ApiKeyRevoked", "api_key_revoked",
				"CertAdded", "cert_added",
				"CertExpired", "cert_expired",
				"CertRenewed", "cert_renewed",
				"CertSelfSignedFallback", "cert_self_signed_fallback",
				"AgentConnected", "agent_connected",
				"AgentDisconnected", "agent_disconnected",
				"AgentConfigSynced", "agent_config_synced",
			),
		field.String("client_id").Optional().MaxLen(64),
		field.String("plan_id").Optional().MaxLen(64),
		field.String("node_id").Optional().MaxLen(64),
		field.String("agent_id").Optional().MaxLen(64),
		field.String("reseller_id").Optional().MaxLen(64),
		field.JSON("tags", []string{}).Optional(),
		field.JSON("metadata", map[string]any{}).Optional(),
		field.Time("ts").
			Comment("partition key — present in every query for pruning"),
	}
}

func (Event) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("ts"),
		index.Fields("type", "ts"),
		index.Fields("client_id", "ts"),
		index.Fields("reseller_id", "ts"),
		index.Fields("agent_id", "ts"),
	}
}
