package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// DomainCertificate is the cross-system TLS material store. Private
// keys are AES-256-GCM-encrypted at rest using HUE_PASSWORD_ENC_KEY.
//
// Domain matching: exact ("api.example.com"), wildcard
// ("*.example.com" → single-label subdomains), or IP-as-host.
//
// Issuers: SELF_SIGNED (HUE auto-generated for own domain when secure
// flag set with no real cert), ACME (Let's Encrypt), IMPORTED.
type DomainCertificate struct{ ent.Schema }

func (DomainCertificate) Mixin() []ent.Mixin { return []ent.Mixin{UUIDMixin{}, TimeMixin{}} }

func (DomainCertificate) Fields() []ent.Field {
	return []ent.Field{
		field.JSON("domain_names", []string{}),
		field.Text("public_key_pem").NotEmpty(),
		field.Bytes("private_key_ciphertext").Sensitive(),
		field.Uint8("private_key_key_id").Optional().Default(0),
		field.Time("expires_at"),
		field.Enum("issuer").
			NamedValues(
				"SelfSigned", "self_signed",
				"Acme", "acme",
				"Imported", "imported",
			),
		field.String("generated_node_id").Optional().MaxLen(64),
		field.Bool("valid").Default(true),
	}
}

func (DomainCertificate) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("expires_at"),
		index.Fields("issuer"),
		index.Fields("valid"),
	}
}
