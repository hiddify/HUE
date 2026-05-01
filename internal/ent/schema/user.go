package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// User is a tenant of the system that consumes traffic.
//
// password_hash, public_key and private_key_enc are stored separately from
// the cleartext fields the proto exposes — proto carries plaintext for
// service-provisioning needs, but persistence is hashed/encrypted.
type User struct{ ent.Schema }

func (User) Mixin() []ent.Mixin {
	return []ent.Mixin{UUIDMixin{}, TimeMixin{}}
}

func (User) Fields() []ent.Field {
	return []ent.Field{
		field.String("username").
			NotEmpty().
			MaxLen(64).
			Unique(),
		field.String("password_hash").
			Optional().
			Sensitive().
			MaxLen(256),
		field.String("public_key").
			Optional().
			MaxLen(16384),
		field.String("private_key_enc").
			Optional().
			Sensitive().
			MaxLen(32768),
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
		field.UUID("manager_id", uuid.UUID{}).Optional().Nillable(),
		field.UUID("active_plan_id", uuid.UUID{}).Optional().Nillable(),
	}
}

func (User) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("manager", Manager.Type).
			Ref("users").
			Unique().
			Field("manager_id"),
		edge.To("usage_plans", UsagePlan.Type),
		edge.To("active_plan", UsagePlan.Type).
			Unique().
			Field("active_plan_id"),
	}
}

// Indexes covers the common access patterns: filtering by status and
// time-window scans on last_connection_at for "online users".
func (User) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("status"),
		index.Fields("last_connection_at"),
	}
}
