package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// Event is the immutable audit log row. Stored in a PostgreSQL table that is
// PARTITIONED BY RANGE (ts) at the migration level — ent doesn't model the
// partition itself, but the schema must declare ts as part of every query
// shape so partition pruning works.
//
// ID is UUID; the migration declares PRIMARY KEY (id, ts) since PostgreSQL
// requires the partition key to be in the PK.
type Event struct{ ent.Schema }

func (Event) Mixin() []ent.Mixin { return []ent.Mixin{UUIDMixin{}} }

func (Event) Fields() []ent.Field {
	return []ent.Field{
		field.Enum("type").
			NamedValues(
				"UserConnected", "user_connected",
				"UserDisconnected", "user_disconnected",
				"UsageRecorded", "usage_recorded",
				"UsagePlanExpired", "usage_plan_expired",
				"UsagePlanQuotaUsed", "usage_plan_quota_used",
				"NodeReset", "node_reset",
				"ManagerExpired", "manager_expired",
				"PenaltyApplied", "penalty_applied",
				"PenaltyExpired", "penalty_expired",
				"UserSuspended", "user_suspended",
				"UserActivated", "user_activated",
				"ManagerLimitReached", "manager_limit_reached",
				"UsagePlanStarted", "usage_plan_started",
				"ManagerPlanStarted", "manager_plan_started",
				"ServiceKeyShared", "service_key_shared",
			),
		field.String("user_id").Optional().MaxLen(64),
		field.String("plan_id").Optional().MaxLen(64),
		field.String("node_id").Optional().MaxLen(64),
		field.String("service_id").Optional().MaxLen(64),
		field.String("manager_id").Optional().MaxLen(64),
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
		index.Fields("user_id", "ts"),
		index.Fields("manager_id", "ts"),
	}
}
