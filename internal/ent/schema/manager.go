package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// Manager is a tree node in the reseller hierarchy. Limits enforced at any
// level cap the descendants' aggregated usage.
type Manager struct{ ent.Schema }

func (Manager) Mixin() []ent.Mixin { return []ent.Mixin{UUIDMixin{}, TimeMixin{}} }

func (Manager) Fields() []ent.Field {
	return []ent.Field{
		field.String("name").NotEmpty().MaxLen(128),
		field.Enum("status").
			NamedValues("Active", "active", "Inactive", "inactive").
			Default("active"),
		field.JSON("metadata", map[string]any{}).Optional(),
		field.UUID("parent_id", uuid.UUID{}).Optional().Nillable(),
	}
}

func (Manager) Edges() []ent.Edge {
	return []ent.Edge{
		// Self-referential parent ↔ children.
		edge.To("children", Manager.Type).
			From("parent").
			Unique().
			Field("parent_id"),
		edge.To("users", User.Type),
		edge.To("plan", ManagerPlan.Type).Unique(),
	}
}

func (Manager) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("parent_id"),
		index.Fields("status"),
	}
}
