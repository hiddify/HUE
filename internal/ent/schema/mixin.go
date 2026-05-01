// Package schema defines the entity schemas consumed by `ent generate`.
package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/mixin"
	"github.com/google/uuid"
)

// UUIDMixin gives every resource a server-generated UUIDv4 id, immutable.
type UUIDMixin struct{ mixin.Schema }

func (UUIDMixin) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).
			Default(uuid.New).
			Immutable(),
	}
}

// TimeMixin gives every resource created_at + updated_at timestamps,
// auto-managed by ent so business code never sets them.
type TimeMixin struct{ mixin.Schema }

func (TimeMixin) Fields() []ent.Field {
	return []ent.Field{
		field.Time("created_at").
			Default(time.Now).
			Immutable(),
		field.Time("updated_at").
			Default(time.Now).
			UpdateDefault(time.Now),
	}
}
