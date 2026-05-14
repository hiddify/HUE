package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// Subscriber is a tenant that consumes traffic.
//
// Naming note: the proto + REST + docs use the term "Client"; the ent
// schema is named "Subscriber" because ent reserves the `Client` type
// name for its own database handle (*ent.Client). Translate layer maps
// between the two. Mental model: Subscriber is the persistence type,
// Client is the API noun.
//
// Password storage policy is intentional: downstream protocols
// (trojan, shadowsocks, RADIUS) need the plaintext. HUE keeps an
// AES-256-GCM ciphertext + key_id in `password_ciphertext` /
// `password_key_id`; decrypted on the fly for AuthService.Login and
// ConfigService template render. A DB dump alone discloses nothing
// without HUE_PASSWORD_ENC_KEY.
type Subscriber struct{ ent.Schema }

func (Subscriber) Mixin() []ent.Mixin {
	return []ent.Mixin{UUIDMixin{}, TimeMixin{}}
}

func (Subscriber) Fields() []ent.Field {
	return []ent.Field{
		field.String("username").
			NotEmpty().
			MaxLen(64).
			Unique(),
		field.Bytes("password_ciphertext").
			Optional().
			Sensitive(),
		field.Uint8("password_key_id").
			Optional().
			Default(0),
		field.String("public_key").
			Optional().
			MaxLen(16384),
		field.Bytes("private_key_ciphertext").
			Optional().
			Sensitive(),
		field.JSON("ca_certs", []string{}).
			Optional(),
		field.JSON("allowed_devices", []string{}).
			Optional(),
		field.JSON("groups", []string{}).
			Optional(),
		field.Enum("status").
			NamedValues(
				"Active", "active",
				"Suspended", "suspended",
				"Expired", "expired",
				"QuotaUsed", "quota_used",
				"Penalty", "penalty",
				"Inactive", "inactive",
			).
			Default("active"),
		field.JSON("metadata", map[string]any{}).
			Optional(),
		field.Time("first_connection_at").Optional().Nillable(),
		field.Time("last_connection_at").Optional().Nillable(),
		field.UUID("reseller_id", uuid.UUID{}).Optional().Nillable(),
		field.UUID("active_plan_id", uuid.UUID{}).Optional().Nillable(),
	}
}

func (Subscriber) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("reseller", Reseller.Type).
			Ref("subscribers").
			Unique().
			Field("reseller_id"),
		edge.To("usage_plans", UsagePlan.Type),
		edge.To("active_plan", UsagePlan.Type).
			Unique().
			Field("active_plan_id"),
	}
}

func (Subscriber) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("status"),
		index.Fields("last_connection_at"),
		index.Fields("reseller_id"),
	}
}
