package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// UsageReport is the raw, denormalized usage row written by the buffered
// flush. Like Event, this table is PARTITIONED BY RANGE (ts) in the
// migration. Indexes are deliberately minimal — write-heavy table, reads go
// through aggregates.
type UsageReport struct{ ent.Schema }

func (UsageReport) Mixin() []ent.Mixin { return []ent.Mixin{UUIDMixin{}} }

func (UsageReport) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("user_id", uuid.UUID{}),
		field.UUID("node_id", uuid.UUID{}).Optional().Nillable(),
		field.UUID("service_id", uuid.UUID{}).Optional().Nillable(),
		field.Int64("upload").NonNegative().Default(0),
		field.Int64("download").NonNegative().Default(0),
		field.String("session_id").Optional().MaxLen(128),
		field.JSON("tags", []string{}).Optional(),
		field.Time("ts").Comment("partition key"),
	}
}

func (UsageReport) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("user_id", "ts"),
		index.Fields("node_id", "ts"),
	}
}
