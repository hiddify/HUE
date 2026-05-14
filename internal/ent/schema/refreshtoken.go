package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// RefreshToken backs AuthService.Refresh. Token value the caller holds
// is opaque (random base32); only its sha256 lives here as `jti`.
// Rotation: every Refresh issues new token and sets `replaced_by` on
// old row — reuse becomes detectable.
type RefreshToken struct{ ent.Schema }

func (RefreshToken) Mixin() []ent.Mixin { return []ent.Mixin{UUIDMixin{}, TimeMixin{}} }

func (RefreshToken) Fields() []ent.Field {
	return []ent.Field{
		field.String("jti").NotEmpty().Unique().MaxLen(128),
		field.Enum("principal_kind").
			NamedValues(
				"Client", "client",
				"Reseller", "reseller",
				"Owner", "owner",
			),
		field.String("principal_id").NotEmpty().MaxLen(64),
		field.Time("expires_at"),
		field.Time("revoked_at").Optional().Nillable(),
		field.String("replaced_by").Optional().MaxLen(128),
		field.Bool("owner_sudo").Default(false),
	}
}

func (RefreshToken) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("principal_kind", "principal_id"),
		index.Fields("expires_at"),
		index.Fields("revoked_at"),
	}
}
