package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// ApiKey authenticates Owners (root) and Agents (machine adapters).
// Clients + Resellers use JWT via AuthService instead and do NOT
// have ApiKey rows.
//
// Verification flow:
//   1. Bearer → LookupPrefix → SELECT by prefix (unique).
//   2. Argon2id verify.
//   3. Reject if revoked_at OR expires_at <= now.
//   4. Attach actor (kind=owner|agent, agent_id) to ctx.
type ApiKey struct{ ent.Schema }

func (ApiKey) Mixin() []ent.Mixin { return []ent.Mixin{UUIDMixin{}, TimeMixin{}} }

func (ApiKey) Fields() []ent.Field {
	return []ent.Field{
		field.Enum("kind").
			NamedValues(
				"Owner", "owner",
				"Agent", "agent",
			),
		field.String("agent_id").Optional().MaxLen(64),
		field.String("name").NotEmpty().MaxLen(128),
		field.String("prefix").
			NotEmpty().
			MaxLen(32).
			Unique().
			Comment("non-secret routing key inside the bearer token"),
		field.String("hash").
			NotEmpty().
			Sensitive().
			MaxLen(256).
			Comment("argon2id encoded hash"),
		field.Time("last_used_at").Optional().Nillable(),
		field.Time("revoked_at").Optional().Nillable(),
		field.Time("expires_at").Optional().Nillable(),
	}
}

func (ApiKey) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("kind", "agent_id"),
		index.Fields("revoked_at"),
		index.Fields("expires_at"),
	}
}
