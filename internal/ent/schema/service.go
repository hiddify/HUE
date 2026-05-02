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
		// Full config template — the literal protocol-native config
		// (xray JSON, wireguard ini, …) the renderer fills in at
		// SyncConfig time. Stored as text since it can be quite long.
		field.Text("config_template").Optional(),
		// Hint for the renderer to pick the right substitution
		// dialect. Today: "xray-json" | "wireguard-ini" | "openvpn-conf".
		field.String("config_template_format").Optional().MaxLen(32),
		// Small per-service overrides referenced from the template via
		// ${vars.<key>} or {{.Vars.<key>}}. Keys MAY be dotted; the
		// renderer treats them as flat strings.
		field.JSON("config_vars", map[string]string{}).Optional(),
		// Etag is updated whenever any of {template, format, vars}
		// changes; SyncConfig short-circuits when caller's etag matches.
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
