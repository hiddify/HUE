package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// Node is a virtual grouping of Agents on one host. Owns the config
// map (per-node config replaces per-service from phase 1) and the
// bandwidth ceiling shared by all agents on the node.
type Node struct{ ent.Schema }

func (Node) Mixin() []ent.Mixin { return []ent.Mixin{UUIDMixin{}, TimeMixin{}} }

func (Node) Fields() []ent.Field {
	return []ent.Field{
		field.String("name").NotEmpty().MaxLen(128),
		field.JSON("ips", []string{}).Optional(),
		field.JSON("allowed_cidrs", []string{}).Optional(),
		field.Float("traffic_multiplier").Default(1.0),
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
		field.Int64("current_total").NonNegative().Default(0),
		field.Int64("current_upload").NonNegative().Default(0),
		field.Int64("current_download").NonNegative().Default(0),
		field.Int64("bandwidth_limit_bytes").NonNegative().Default(0),
		// Per-node config map. Keys are dotted (e.g.
		// "xray.numeric_version.25003007000"). Values are JSON-typed
		// arbitrary structures; the agent kind+version picks at
		// SyncConfig time.
		field.JSON("config", map[string]any{}).Optional(),
		field.JSON("service_hostnames", []string{}).Optional(),
		field.String("country").Optional().MaxLen(2),
		field.String("city").Optional().MaxLen(128),
		field.String("isp").Optional().MaxLen(256),
		field.Uint32("asn").Optional(),
		field.Enum("status").
			NamedValues("Active", "active", "Disabled", "disabled").
			Default("active"),
	}
}

func (Node) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("agents", Agent.Type),
	}
}

func (Node) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("status"),
	}
}
