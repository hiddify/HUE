package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// Reseller is a tree of human admins authenticated via username +
// Argon2id-hashed password (AuthService.Login → JWT). Resellers manage
// child resellers + clients under them; recursive scope enforced by
// the auth interceptor.
type Reseller struct{ ent.Schema }

func (Reseller) Mixin() []ent.Mixin {
	return []ent.Mixin{UUIDMixin{}, TimeMixin{}}
}

func (Reseller) Fields() []ent.Field {
	return []ent.Field{
		field.String("name").
			NotEmpty().
			MaxLen(128).
			Unique(),
		field.String("display_name").
			Optional().
			MaxLen(128),
		field.String("password_hash").
			NotEmpty().
			Sensitive().
			MaxLen(256),
		field.Enum("status").
			NamedValues("Active", "active", "Inactive", "inactive").
			Default("active"),
		field.JSON("metadata", map[string]any{}).Optional(),
		field.UUID("parent_id", uuid.UUID{}).Optional().Nillable(),
	}
}

func (Reseller) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("children", Reseller.Type).
			From("parent").
			Unique().
			Field("parent_id"),
		edge.To("subscribers", Subscriber.Type),
		edge.To("plan", ResellerPlan.Type).Unique(),
	}
}

func (Reseller) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("parent_id"),
		index.Fields("status"),
	}
}
