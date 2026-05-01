package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// ApiKey is a per-actor authentication credential. The plaintext token is
// returned exactly once at creation time; only its Argon2id hash is stored.
// Verification flow:
//   1. Caller presents `Authorization: Bearer <prefix>_<rest>`.
//   2. Server looks up by `prefix` (indexed, unique).
//   3. Server runs Argon2id verify on the full token vs `hash`.
//   4. Server checks revoked_at == NULL.
type ApiKey struct{ ent.Schema }

func (ApiKey) Mixin() []ent.Mixin { return []ent.Mixin{UUIDMixin{}, TimeMixin{}} }

func (ApiKey) Fields() []ent.Field {
	return []ent.Field{
		field.Enum("kind").
			NamedValues(
				"Manager", "manager",
				"Service", "service",
				"Node", "node",
			),
		field.String("owner_id").NotEmpty().MaxLen(64),
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
	}
}

func (ApiKey) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("kind", "owner_id"),
		index.Fields("revoked_at"),
	}
}
