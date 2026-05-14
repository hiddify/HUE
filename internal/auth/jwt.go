package auth

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/hiddify/hue/internal/ent"
	entsigningkey "github.com/hiddify/hue/internal/ent/signingkey"
)

// JWT — minimal stdlib implementation (header + payload + ed25519
// signature, base64url-encoded). No third-party JWT library needed
// since we only sign/verify our own tokens.
//
// Phase-2 token shape:
//   header  = {"alg":"EdDSA","typ":"JWT","kid":"<signing_key uuid>"}
//   payload = {
//     "sub":  "<subject id>",   // Subscriber.id or Reseller.id (or "" for Owner)
//     "kind": "subscriber|reseller|owner",
//     "exp":  <unix>,
//     "iat":  <unix>,
//     "jti":  "<token id>",
//     "sudo": <bool>,           // true when issued via Owner sudo
//     "tgt":  "<sudo target id>" // when sudo
//   }

const (
	defaultAccessTTL  = 15 * time.Minute
	defaultRefreshTTL = 30 * 24 * time.Hour
)

// Claims is the JWT payload.
type Claims struct {
	Subject      string `json:"sub"`
	Kind         string `json:"kind"`
	ExpiresAt    int64  `json:"exp"`
	IssuedAt     int64  `json:"iat"`
	JTI          string `json:"jti"`
	OwnerSudo    bool   `json:"sudo,omitempty"`
	SudoTargetID string `json:"tgt,omitempty"`
}

// SigningKeyMaterial pairs the DB row's id with its raw ed25519 keys.
type SigningKeyMaterial struct {
	ID         uuid.UUID
	PrivateKey ed25519.PrivateKey
	PublicKey  ed25519.PublicKey
}

// EnsureSigningKey returns the active signing key from the DB. On a
// fresh database with no active key, it auto-generates an ed25519
// keypair and persists it. Idempotent.
func EnsureSigningKey(ctx context.Context, db *ent.Client) (*SigningKeyMaterial, error) {
	row, err := db.SigningKey.Query().
		Where(entsigningkey.Active(true), entsigningkey.RevokedAtIsNil()).
		First(ctx)
	if err != nil && !ent.IsNotFound(err) {
		return nil, fmt.Errorf("auth: load signing key: %w", err)
	}
	if row != nil {
		return decodeSigningKey(row)
	}

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("auth: generate signing key: %w", err)
	}
	encrypted, keyID, err := Encrypt(priv)
	if err != nil {
		return nil, err
	}
	saved, err := db.SigningKey.Create().
		SetAlgorithm(entsigningkey.AlgorithmEd25519).
		SetPrivateKeyCiphertext(encrypted).
		SetPrivateKeyKeyID(keyID).
		SetPublicKey(pub).
		SetActive(true).
		Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("auth: persist signing key: %w", err)
	}
	return &SigningKeyMaterial{ID: saved.ID, PrivateKey: priv, PublicKey: pub}, nil
}

func decodeSigningKey(row *ent.SigningKey) (*SigningKeyMaterial, error) {
	priv, err := Decrypt(row.PrivateKeyCiphertext, row.PrivateKeyKeyID)
	if err != nil {
		return nil, fmt.Errorf("auth: decrypt signing key: %w", err)
	}
	if len(priv) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("auth: signing key wrong size: %d", len(priv))
	}
	return &SigningKeyMaterial{
		ID:         row.ID,
		PrivateKey: ed25519.PrivateKey(priv),
		PublicKey:  ed25519.PublicKey(row.PublicKey),
	}, nil
}

// SignClaims returns a base64url-encoded EdDSA JWT.
func SignClaims(key *SigningKeyMaterial, c Claims) (string, error) {
	headerJSON, err := json.Marshal(map[string]string{
		"alg": "EdDSA",
		"typ": "JWT",
		"kid": key.ID.String(),
	})
	if err != nil {
		return "", err
	}
	payloadJSON, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	enc := base64.RawURLEncoding
	signingInput := enc.EncodeToString(headerJSON) + "." + enc.EncodeToString(payloadJSON)
	sig := ed25519.Sign(key.PrivateKey, []byte(signingInput))
	return signingInput + "." + enc.EncodeToString(sig), nil
}

// VerifyToken validates token and returns the parsed Claims on
// success. The verifier looks up the kid in the DB so old tokens keep
// working after a key rotation (as long as the old key isn't revoked).
func VerifyJWT(ctx context.Context, db *ent.Client, token string) (Claims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return Claims{}, errors.New("auth: malformed JWT")
	}
	enc := base64.RawURLEncoding
	headerBytes, err := enc.DecodeString(parts[0])
	if err != nil {
		return Claims{}, fmt.Errorf("auth: decode header: %w", err)
	}
	var header struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	if err := json.Unmarshal(headerBytes, &header); err != nil {
		return Claims{}, fmt.Errorf("auth: parse header: %w", err)
	}
	if header.Alg != "EdDSA" {
		return Claims{}, fmt.Errorf("auth: unsupported alg %q", header.Alg)
	}
	kid, err := uuid.Parse(header.Kid)
	if err != nil {
		return Claims{}, fmt.Errorf("auth: bad kid: %w", err)
	}
	row, err := db.SigningKey.Query().
		Where(entsigningkey.ID(kid), entsigningkey.RevokedAtIsNil()).
		Only(ctx)
	if err != nil {
		return Claims{}, fmt.Errorf("auth: unknown signing key: %w", err)
	}
	sig, err := enc.DecodeString(parts[2])
	if err != nil {
		return Claims{}, fmt.Errorf("auth: decode sig: %w", err)
	}
	signingInput := []byte(parts[0] + "." + parts[1])
	if !ed25519.Verify(ed25519.PublicKey(row.PublicKey), signingInput, sig) {
		return Claims{}, errors.New("auth: bad signature")
	}
	payloadBytes, err := enc.DecodeString(parts[1])
	if err != nil {
		return Claims{}, fmt.Errorf("auth: decode payload: %w", err)
	}
	var c Claims
	if err := json.Unmarshal(payloadBytes, &c); err != nil {
		return Claims{}, fmt.Errorf("auth: parse payload: %w", err)
	}
	if time.Now().Unix() > c.ExpiresAt {
		return Claims{}, errors.New("auth: token expired")
	}
	return c, nil
}

// NewRefreshToken returns a fresh opaque refresh-token plaintext + its
// sha256 hash (the `jti` stored in DB).
func NewRefreshToken() (plaintext, jti string, err error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", "", err
	}
	plaintext = base64.RawURLEncoding.EncodeToString(raw)
	sum := sha256.Sum256([]byte(plaintext))
	jti = hex.EncodeToString(sum[:])
	return plaintext, jti, nil
}

// HashRefresh hashes a plaintext refresh token for DB lookup.
func HashRefresh(plaintext string) string {
	sum := sha256.Sum256([]byte(plaintext))
	return hex.EncodeToString(sum[:])
}

// DefaultAccessTTL returns the access-token lifetime used by AuthService.
func DefaultAccessTTL() time.Duration { return defaultAccessTTL }

// DefaultRefreshTTL returns the refresh-token lifetime used by AuthService.
func DefaultRefreshTTL() time.Duration { return defaultRefreshTTL }
