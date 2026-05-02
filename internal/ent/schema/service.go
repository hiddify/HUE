package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// Service is a specific protocol instance (vless, wireguard, ...) on a Node.
type Service struct{ ent.Schema }

func (Service) Mixin() []ent.Mixin { return []ent.Mixin{UUIDMixin{}, TimeMixin{}} }

func (Service) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("node_id", uuid.UUID{}),
		field.String("name").NotEmpty().MaxLen(128),
		field.Enum("protocol").
			NamedValues(
				"Vless", "vless",
				"Vmess", "vmess",
				"Trojan", "trojan",
				"Shadowsocks", "shadowsocks",
				"Wireguard", "wireguard",
				"Openvpn", "openvpn",
				"Ipsec", "ipsec",
				"L2tp", "l2tp",
				"Pptp", "pptp",
				"Ppp", "ppp",
				"Sstp", "sstp",
				"Ssh", "ssh",
				"Radius", "radius",
				"Xray", "xray",
				"Singbox", "singbox",
			),
		// JSON list of allowed auth method names: uuid|password|public_key|certificate.
		field.JSON("allowed_auth_methods", []string{}).Optional(),
		field.String("callback_url").Optional().MaxLen(512),
		field.Int64("current_total").NonNegative().Default(0),
		field.Int64("current_upload").NonNegative().Default(0),
		field.Int64("current_download").NonNegative().Default(0),
		// Per-service abstract key-value config — interpreted by the
		// matching pkg/clients/<protocol> ConfigGenerator on the client
		// side. Stored as JSONB so values can be opaque blobs (PEM,
		// JSON-encoded substructures, …).
		field.JSON("config", map[string]string{}).Optional(),
		// Etag is updated whenever config changes; SyncConfig short-
		// circuits when caller's etag matches.
		field.String("config_etag").Optional().MaxLen(64),
	}
}

func (Service) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("node", Node.Type).
			Ref("services").
			Unique().
			Required().
			Field("node_id"),
	}
}

func (Service) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("node_id"),
		index.Fields("protocol"),
	}
}
