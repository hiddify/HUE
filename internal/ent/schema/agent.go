package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// Agent is a process on a Node (xray, wireguard, …) that consumes
// HUE's gRPC API via a per-agent API key. Bandwidth quota is enforced
// at the Node level — many agents share one node-wide cap.
type Agent struct{ ent.Schema }

func (Agent) Mixin() []ent.Mixin { return []ent.Mixin{UUIDMixin{}, TimeMixin{}} }

func (Agent) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("node_id", uuid.UUID{}),
		field.String("name").NotEmpty().MaxLen(128),
		field.Enum("kind").
			NamedValues(
				"Xray", "xray",
				"Singbox", "singbox",
				"Wireguard", "wireguard",
				"Openvpn", "openvpn",
				"Ipsec", "ipsec",
				"Radius", "radius",
				"Ssh", "ssh",
			),
		field.String("version").Optional().MaxLen(64),
		field.Time("last_seen_at").Optional().Nillable(),
		field.Int64("current_total").NonNegative().Default(0),
		field.Int64("current_upload").NonNegative().Default(0),
		field.Int64("current_download").NonNegative().Default(0),
	}
}

func (Agent) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("node", Node.Type).
			Ref("agents").
			Unique().
			Required().
			Field("node_id"),
	}
}

func (Agent) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("node_id"),
		index.Fields("kind"),
	}
}
