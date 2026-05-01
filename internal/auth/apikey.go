// Package auth provides API-key issuance, verification, and a gRPC
// interceptor that attaches an Actor to every request context.
//
// Token format: "<kind_prefix>_<base32 body>" where:
//   * kind_prefix is "mgr", "svc", or "nod" — chosen so the server can
//     route to the right table without first hashing.
//   * body is 24 bytes of OS randomness, base32-encoded (~38 chars,
//     lowercased, unpadded). 192 bits of entropy.
//
// A LookupPrefix (kind_prefix + first 8 chars of body) is stored separately
// from the Argon2id-encoded hash. Auth flow:
//   1. Receive Authorization: Bearer <token>.
//   2. Compute LookupPrefix(token) → DB index seek (unique).
//   3. Argon2id verify the full token against the stored encoded hash.
//   4. Reject if revoked_at IS NOT NULL.
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base32"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// ActorKind identifies what kind of principal an API key belongs to.
type ActorKind string

const (
	KindManager ActorKind = "manager"
	KindService ActorKind = "service"
	KindNode    ActorKind = "node"

	prefixManager = "mgr"
	prefixService = "svc"
	prefixNode    = "nod"

	bodyRandBytes  = 24
	prefixBodyChrs = 8 // chars of base32 body retained in lookup prefix
)

// argon2id parameters tuned for API-key verify (~5ms on a modern CPU).
// Tokens carry 192 bits of entropy already, so the slowdown is
// defense-in-depth, not the primary security control.
const (
	argonTime    uint32 = 2
	argonMemory  uint32 = 16 * 1024 // KiB
	argonThreads uint8  = 1
	argonKeyLen  uint32 = 32
	argonSaltLen        = 16
)

var (
	ErrUnknownKind   = errors.New("auth: unknown actor kind")
	ErrMalformedKey  = errors.New("auth: malformed api key")
	ErrInvalidHash   = errors.New("auth: invalid stored hash")
	ErrVersionMisma  = errors.New("auth: argon2 version mismatch")
	ErrInvalidParams = errors.New("auth: invalid argon2 params")
)

// PrefixForKind returns the kind portion of a token prefix.
func PrefixForKind(kind ActorKind) (string, error) {
	switch kind {
	case KindManager:
		return prefixManager, nil
	case KindService:
		return prefixService, nil
	case KindNode:
		return prefixNode, nil
	}
	return "", ErrUnknownKind
}

// KindForPrefix is the inverse of PrefixForKind, called during lookup.
func KindForPrefix(p string) (ActorKind, bool) {
	switch p {
	case prefixManager:
		return KindManager, true
	case prefixService:
		return KindService, true
	case prefixNode:
		return KindNode, true
	}
	return "", false
}

// GenerateKey issues a new API key. Returns:
//   * lookupPrefix — store in api_keys.prefix; uniquely identifies the row
//   * plaintext    — return to the caller exactly once; never stored
//   * encodedHash  — store in api_keys.hash
func GenerateKey(kind ActorKind) (lookupPrefix, plaintext, encodedHash string, err error) {
	kp, err := PrefixForKind(kind)
	if err != nil {
		return "", "", "", err
	}
	raw := make([]byte, bodyRandBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", "", "", fmt.Errorf("auth: read entropy: %w", err)
	}
	body := strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw))
	plaintext = kp + "_" + body
	lookupPrefix = kp + "_" + body[:prefixBodyChrs]

	encodedHash, err = HashToken(plaintext)
	if err != nil {
		return "", "", "", err
	}
	return lookupPrefix, plaintext, encodedHash, nil
}

// LookupPrefix extracts the (kind_prefix + first 8 body chars) portion from
// a presented token. Returns "" if the token is malformed.
func LookupPrefix(token string) string {
	idx := strings.IndexByte(token, '_')
	if idx <= 0 || idx >= len(token)-prefixBodyChrs {
		return ""
	}
	return token[:idx+1+prefixBodyChrs]
}

// HashToken returns the encoded Argon2id hash of plaintext, including
// salt + parameters in PHC string format.
func HashToken(plaintext string) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("auth: read salt: %w", err)
	}
	key := argon2.IDKey([]byte(plaintext), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf(
		"$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version,
		argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	), nil
}

// VerifyToken is constant-time. Returns nil iff plaintext hashes match.
func VerifyToken(plaintext, encoded string) error {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return ErrInvalidHash
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil {
		return ErrInvalidHash
	}
	if version != argon2.Version {
		return ErrVersionMisma
	}
	var memory, time uint32
	var threads uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &time, &threads); err != nil {
		return ErrInvalidParams
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return ErrInvalidHash
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return ErrInvalidHash
	}
	got := argon2.IDKey([]byte(plaintext), salt, time, memory, threads, uint32(len(want)))
	if subtle.ConstantTimeCompare(got, want) != 1 {
		return errors.New("auth: token mismatch")
	}
	return nil
}
