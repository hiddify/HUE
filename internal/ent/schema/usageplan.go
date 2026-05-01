package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// UsagePlan is the per-user quota policy + live counters. A user may have
// multiple plans over time (history), but only one is active at a time
// (referenced by User.active_plan_id).
type UsagePlan struct{ ent.Schema }

func (UsagePlan) Mixin() []ent.Mixin { return []ent.Mixin{UUIDMixin{}, TimeMixin{}} }

func (UsagePlan) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("user_id", uuid.UUID{}),
		// Limits in bytes; 0 means unlimited for the dimension.
		field.Int64("total_limit").NonNegative().Default(0),
		field.Int64("upload_limit").NonNegative().Default(0),
		field.Int64("download_limit").NonNegative().Default(0),
		// Live counters.
		field.Int64("current_total").NonNegative().Default(0),
		field.Int64("current_upload").NonNegative().Default(0),
		field.Int64("current_download").NonNegative().Default(0),
		field.Int32("max_concurrent").NonNegative().Default(0),
		field.Enum("reset_mode").
			NamedValues(
				"NoReset", "no_reset",
				"Hourly", "hourly",
				"Daily", "daily",
				"Weekly", "weekly",
				"Monthly", "monthly",
				"Yearly", "yearly",
			).
			Default("no_reset"),
		field.Time("reset_anchor").Optional().Nillable(),
		field.Int64("duration_seconds").NonNegative().Default(0),
		field.Time("start_at").Optional().Nillable(),
		field.Time("expires_at").Optional().Nillable(),
		field.Enum("status").
			NamedValues(
				"Active", "active",
				"Expired", "expired",
				"QuotaUsed", "quota_used",
				"Suspended", "suspended",
				"SessionReached", "session_reached",
				"Penalty", "penalty",
				"Inactive", "inactive",
			).
			Default("active"),
	}
}

func (UsagePlan) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("user", User.Type).
			Ref("usage_plans").
			Unique().
			Required().
			Field("user_id"),
	}
}

func (UsagePlan) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("user_id"),
		index.Fields("status"),
		index.Fields("expires_at"),
	}
}
