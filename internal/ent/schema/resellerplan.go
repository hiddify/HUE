package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"github.com/google/uuid"
)

// ResellerPlan is per-reseller traffic + concurrency limits with
// aggregated counters. Descendants' usage rolls up here.
type ResellerPlan struct{ ent.Schema }

func (ResellerPlan) Mixin() []ent.Mixin { return []ent.Mixin{UUIDMixin{}, TimeMixin{}} }

func (ResellerPlan) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("reseller_id", uuid.UUID{}),
		field.Int64("total_limit").NonNegative().Default(0),
		field.Int64("upload_limit").NonNegative().Default(0),
		field.Int64("download_limit").NonNegative().Default(0),
		field.Int64("current_total").NonNegative().Default(0),
		field.Int64("current_upload").NonNegative().Default(0),
		field.Int64("current_download").NonNegative().Default(0),
		field.Int32("max_sessions").NonNegative().Default(0),
		field.Int32("max_online_users").NonNegative().Default(0),
		field.Int32("max_active_users").NonNegative().Default(0),
		field.Int32("current_sessions").NonNegative().Default(0),
		field.Int32("current_online_users").NonNegative().Default(0),
		field.Int32("current_active_users").NonNegative().Default(0),
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
			NamedValues("Active", "active", "Inactive", "inactive").
			Default("active"),
	}
}

func (ResellerPlan) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("reseller", Reseller.Type).
			Ref("plan").
			Unique().
			Required().
			Field("reseller_id"),
	}
}
