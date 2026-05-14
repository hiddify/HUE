# Certificates

HUE ships a shared certificate store (`DomainCertificateService`) so
every protocol that needs TLS material pulls from one place. Private
keys are AES-256-GCM-encrypted at rest using `HUE_PASSWORD_ENC_KEY`.

## Issuance modes

| Mode          | Source                                             | Notes |
|---------------|----------------------------------------------------|-------|
| `IMPORTED`    | Operator uploads via `AddCertificate`              | Validated against `ParseAndValidate` before accept |
| `SELF_SIGNED` | HUE generates via `internal/cert.SelfSigned`       | 30-year, ECDSA-P-256, SAN list covers all domains incl. wildcards + IPs |
| `ACME`        | Let's Encrypt via `RequestACME` (lego/v4, HTTP-01) | HUE serves the challenge on its own listener at `/.well-known/acme-challenge/` |

## Domain matching

`GetCertificate(domain)` walks `valid=true` rows and matches on:
- **exact** — `api.example.com` matches `api.example.com`
- **wildcard** — `*.example.com` matches `a.example.com` but **not**
  `a.b.example.com` (RFC 6125 §6.4.3)
- **IP-as-host** — `203.0.113.5` matches a cert with that IP in SANs

When multiple rows match, the first hit wins. Phase 3 may add
"longest-cert-validity wins" semantics; today's behavior is acceptable
since wildcards and exacts rarely overlap.

## Encryption at rest

Same scheme as Client passwords:

```
[keyID (1 byte)] [GCM nonce (12 bytes)] [sealed PEM + tag]
```

`HUE_PASSWORD_ENC_KEY` is a 64-hex-char (32-byte) env. **Required in
production**; the AuthService refuses to issue tokens without it. In
dev / test, leaving it empty falls through to pass-through (plaintext
stored as `ciphertext`); CI sets the env to force the encryption path.

## API surface

```
POST   /v1/domainCerts                  AddCertificate         (Owner)
GET    /v1/domainCerts                  ListCertificates       (Reseller, Owner)
GET    /v1/domainCerts/{domain}         GetCertificate         (Reseller, Owner)
DELETE /v1/domainCerts/{id}             DeleteCertificate      (Owner)
POST   /v1/domainCerts:requestACME      RequestACME            (Owner; stubbed)
```

**Private-key redaction**: `Get` / `List` responses include
`private_key_pem` only when the caller is Owner. Reseller reads see an
empty field. ConfigService.SyncConfig is the only data plane that
hands the decrypted key to an Agent (with the
`agent_id → node_id → service_hostnames` matching scoped to certs
that cover one of the node's hostnames).

## Validation on AddCertificate

`internal/cert.ParseAndValidate` rejects PEM that:
- doesn't decode,
- parses but is already past `NotAfter`,
- is missing any of the supplied `domain_names` from its SAN list /
  IP addresses.

It does NOT yet refuse certs that would "weaken" the store (e.g. a
shorter-validity cert overwriting a longer one). Operators are
expected to manage their own rotation discipline; phase 3 can add
that check.

## Self-signed fallback

For HUE's own listener:
- `HUE_TLS_CERT` + `HUE_TLS_KEY` set → use those (phase-1 path).
- `HUE_SECURE=true` + `HUE_PUBLIC_DOMAIN=hue.example.com` set, no
  matching cert in store → HUE generates a 30-year self-signed via
  `internal/cert.SelfSigned`, stores it as `ISSUER_SELF_SIGNED`, and
  uses it. Emits `CERT_SELF_SIGNED_FALLBACK` event.
- Neither set + wildcard bind → HUE refuses to start (per IP-restriction
  policy; see [phase2-changes.md](phase2-changes.md)).

## Operator workflow examples

```bash
# BYO — paste a real cert
curl -k -H "Authorization: Bearer $OWNER" -H 'Content-Type: application/json' \
  https://hue:8443/v1/domainCerts \
  -d "{\"certificate\":{
        \"domain_names\":[\"vpn.example.com\",\"*.vpn.example.com\"],
        \"public_key_pem\":\"$(cat fullchain.pem | jq -R -s .)\",
        \"private_key_pem\":\"$(cat privkey.pem | jq -R -s .)\"
      }}"

# Read back a cert by domain
curl -k -H "Authorization: Bearer $OWNER" \
  https://hue:8443/v1/domainCerts/vpn.example.com

# List ACME-issued only
curl -k -H "Authorization: Bearer $RESELLER_JWT" \
  "https://hue:8443/v1/domainCerts?issuer=CERT_ISSUER_ACME&valid_only=true"
```

## ACME (Let's Encrypt)

`RequestACME` runs the lego/v4 ACME flow against the configured
directory and stores the result. Configuration:

| Var | Default | Notes |
|---|---|---|
| `HUE_ACME_DIRECTORY_URL` | "" (production) | Override with the staging URL when iterating: `https://acme-staging-v02.api.letsencrypt.org/directory` |
| `HUE_ACME_CONTACT_EMAIL` | "" (RPC disabled) | Required. The RPC returns `Unimplemented` when empty |

Wiring HUE picks up automatically:

- A single process-wide `cert.HTTP01Challenger` is constructed at
  `Run()` start and mounted at `/.well-known/acme-challenge/` on the
  rootHandler before the gateway catch-all.
- Each `RequestACME` call generates a fresh ECDSA-P-256 account key,
  registers, obtains, parses `NotAfter`, AES-GCM-encrypts the
  private key, and inserts a `DomainCertificate` row tagged
  `IssuerAcme`. No account-key persistence yet — phase-3 will cache.
- HUE does **not** auto-renew. The operator is expected to call
  `RequestACME` ahead of expiry, or run an external client and
  `AddCertificate` the result.

```bash
# Issue against Let's Encrypt staging first
curl -k -H "Authorization: Bearer $OWNER" -H 'Content-Type: application/json' \
  https://hue:8443/v1/domainCerts:requestACME \
  -d '{"domain_names":["hue.example.com"],"contact_email":"ops@example.com"}'
```

The request's `contact_email` overrides the env default when set. The reply is the
saved `DomainCertificate` row (private key redacted unless Owner).

For air-gapped or DNS-01-only environments, keep using an external
ACME client (`acme.sh`, `certbot`, `lego`) and `AddCertificate` the
result.
