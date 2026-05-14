package auth

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
)

// Encryption layer for at-rest secrets that must round-trip back to
// plaintext on every read (Client passwords feed downstream protocols;
// cert private keys are loaded into TLS configs).
//
// Wire format on disk: [keyID (1 byte)] [nonce (12 bytes)] [ciphertext+tag].
//
// Forward-compat: keyID lets a future Phase 3 rotation step run two
// keys simultaneously without breaking any existing ciphertext. Phase
// 2 ships with a single env-supplied key (HUE_PASSWORD_ENC_KEY) and
// hard-codes its id to 1.

const (
	currentKeyID byte = 1
	gcmNonceLen       = 12
)

// keyFromEnv reads HUE_PASSWORD_ENC_KEY (64 hex chars = 32 bytes).
// Empty env = encryption disabled (passes through plaintext). Phase 2
// MUST enforce non-empty in production; gated by a runtime check in
// the AuthService constructor.
func keyFromEnv() ([]byte, error) {
	hexKey := os.Getenv("HUE_PASSWORD_ENC_KEY")
	if hexKey == "" {
		return nil, nil
	}
	if len(hexKey) != 64 {
		return nil, fmt.Errorf("auth: HUE_PASSWORD_ENC_KEY must be 64 hex chars (32 bytes); got %d", len(hexKey))
	}
	k, err := hex.DecodeString(hexKey)
	if err != nil {
		return nil, fmt.Errorf("auth: decode HUE_PASSWORD_ENC_KEY: %w", err)
	}
	return k, nil
}

// Encrypt seals plaintext under the env-supplied master key. Returns
// ciphertext prefixed with [keyID, nonce]. Empty plaintext returns
// nil — empty stays empty so "no password set" is distinguishable
// from "empty plaintext encrypted".
func Encrypt(plaintext []byte) ([]byte, byte, error) {
	if len(plaintext) == 0 {
		return nil, 0, nil
	}
	key, err := keyFromEnv()
	if err != nil {
		return nil, 0, err
	}
	if key == nil {
		// Disabled — pass through. Phase 2.3 callers (AuthService.Login,
		// ConfigService.SyncConfig) MUST refuse to start in production
		// without the env var set; for dev/tests this branch matters.
		return plaintext, 0, nil
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, 0, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, 0, err
	}
	nonce := make([]byte, gcmNonceLen)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, 0, err
	}
	sealed := aead.Seal(nil, nonce, plaintext, nil)

	out := make([]byte, 0, 1+gcmNonceLen+len(sealed))
	out = append(out, currentKeyID)
	out = append(out, nonce...)
	out = append(out, sealed...)
	return out, currentKeyID, nil
}

// Decrypt is the inverse. Returns plaintext or error. Empty input →
// empty output (no error).
func Decrypt(ciphertext []byte, keyID byte) ([]byte, error) {
	if len(ciphertext) == 0 {
		return nil, nil
	}
	key, err := keyFromEnv()
	if err != nil {
		return nil, err
	}
	if key == nil {
		// Disabled — assume stored plaintext. Same caveat as Encrypt.
		return ciphertext, nil
	}
	// Format: [keyID, nonce, ciphertext+tag]
	if len(ciphertext) < 1+gcmNonceLen {
		// Legacy / pass-through plaintext from disabled-encryption
		// inserts. Treat as plaintext when keyID == 0 was stored.
		if keyID == 0 {
			return ciphertext, nil
		}
		return nil, errors.New("auth: ciphertext shorter than nonce")
	}
	if ciphertext[0] != keyID {
		return nil, fmt.Errorf("auth: keyID mismatch: payload=%d row=%d", ciphertext[0], keyID)
	}
	nonce := ciphertext[1 : 1+gcmNonceLen]
	sealed := ciphertext[1+gcmNonceLen:]
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return aead.Open(nil, nonce, sealed, nil)
}
