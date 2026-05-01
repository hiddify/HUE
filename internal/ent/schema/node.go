package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// Node is a logical grouping of services, typically one VPN server.
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
		edge.To("services", Service.Type),
	}
}

func (Node) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("status"),
	}
}
