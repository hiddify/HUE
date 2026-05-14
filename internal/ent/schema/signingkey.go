package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// SigningKey is HUE's JWT signing material. Auto-generated as ed25519
// at first boot and persisted here. Verifiers iterate all non-revoked
// rows so old JWTs keep working after a key rotation until they
// naturally expire.
//
// Rotation procedure (phase 3, sketched for forward-compat):
//   1. Insert a new active key.
//   2. Mark old key inactive but DON'T revoke (still verifies).
//   3. After max access-token TTL, set revoked_at on old key.
type SigningKey struct{ ent.Schema }

func (SigningKey) Mixin() []ent.Mixin { return []ent.Mixin{UUIDMixin{}, TimeMixin{}} }

func (SigningKey) Fields() []ent.Field {
	return []ent.Field{
		field.Enum("algorithm").
			NamedValues("Ed25519", "ed25519").
			Default("ed25519"),
		field.Bytes("private_key_ciphertext").Sensitive(),
		field.Uint8("private_key_key_id").Optional().Default(0),
		// Raw 32-byte ed25519 public key (plaintext for fast verify).
		field.Bytes("public_key"),
		// At most one row should have active=true at a time. App code
		// enforces; partial unique indexes aren't in ent's schema lang.
		field.Bool("active").Default(true),
		field.Time("revoked_at").Optional().Nillable(),
	}
}

func (SigningKey) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("active"),
		index.Fields("revoked_at"),
	}
}
