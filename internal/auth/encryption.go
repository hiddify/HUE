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
	"strconv"
	"strings"
)

// Encryption layer for at-rest secrets that must round-trip back to
// plaintext on every read (Client passwords, cert private keys).
//
// Wire format: [keyID (1 byte)] [nonce (12 bytes)] [ciphertext+tag].
//
// Rotation — set both env vars before restarting HUE. HUE decrypts
// old ciphertext with the previous key and re-encrypts on next write.
//
// Primary env var (backward compat, always keyID=1):
//
//	HUE_PASSWORD_ENC_KEY=<64 hex chars>
//
// Multi-key rotation via explicit map (overrides the primary var):
//
//	HUE_ENC_KEYS=1:<64hex>,2:<64hex>
//	HUE_ENC_KEY_CURRENT=2
//
// When HUE_ENC_KEYS is present it takes precedence. keyIDs are 1–255.
// keyID 0 is reserved for the "encryption disabled / plaintext" path.

const gcmNonceLen = 12

// keyring holds all known keys + the ID to use for new encryptions.
type keyring struct {
	keys    map[byte][]byte // keyID → 32-byte key
	current byte            // ID used by Encrypt
}

// loadKeyring reads the env and returns the active keyring. Returns a
// zero keyring (encryption disabled) when no env vars are set.
func loadKeyring() (*keyring, error) {
	// Multi-key path.
	if raw := os.Getenv("HUE_ENC_KEYS"); raw != "" {
		return parseKeyring(raw, os.Getenv("HUE_ENC_KEY_CURRENT"))
	}
	// Legacy single-key path — backward compat with Phase 2.
	if hexKey := os.Getenv("HUE_PASSWORD_ENC_KEY"); hexKey != "" {
		k, err := decodeHexKey(hexKey, "HUE_PASSWORD_ENC_KEY")
		if err != nil {
			return nil, err
		}
		return &keyring{keys: map[byte][]byte{1: k}, current: 1}, nil
	}
	// Disabled.
	return &keyring{keys: map[byte][]byte{}}, nil
}

// parseKeyring parses "1:<64hex>,2:<64hex>" and picks the current key.
func parseKeyring(raw, currentStr string) (*keyring, error) {
	kr := &keyring{keys: make(map[byte][]byte)}
	var maxID byte
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		idx := strings.Index(part, ":")
		if idx < 0 {
			return nil, fmt.Errorf("auth: HUE_ENC_KEYS entry %q missing colon", part)
		}
		idStr, hexKey := part[:idx], part[idx+1:]
		n, err := strconv.ParseUint(idStr, 10, 8)
		if err != nil || n == 0 {
			return nil, fmt.Errorf("auth: HUE_ENC_KEYS keyID %q must be 1–255", idStr)
		}
		id := byte(n)
		k, err := decodeHexKey(hexKey, fmt.Sprintf("HUE_ENC_KEYS[%d]", id))
		if err != nil {
			return nil, err
		}
		kr.keys[id] = k
		if id > maxID {
			maxID = id
		}
	}
	if len(kr.keys) == 0 {
		return nil, errors.New("auth: HUE_ENC_KEYS is set but contains no valid entries")
	}
	if currentStr == "" {
		kr.current = maxID // default: highest ID is the current key
		return kr, nil
	}
	n, err := strconv.ParseUint(currentStr, 10, 8)
	if err != nil || n == 0 {
		return nil, fmt.Errorf("auth: HUE_ENC_KEY_CURRENT %q must be 1–255", currentStr)
	}
	kr.current = byte(n)
	if _, ok := kr.keys[kr.current]; !ok {
		return nil, fmt.Errorf("auth: HUE_ENC_KEY_CURRENT=%d not present in HUE_ENC_KEYS", kr.current)
	}
	return kr, nil
}

func decodeHexKey(hexKey, name string) ([]byte, error) {
	if len(hexKey) != 64 {
		return nil, fmt.Errorf("auth: %s must be 64 hex chars (32 bytes); got %d", name, len(hexKey))
	}
	k, err := hex.DecodeString(hexKey)
	if err != nil {
		return nil, fmt.Errorf("auth: decode %s: %w", name, err)
	}
	return k, nil
}

func gcmForKey(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// Encrypt seals plaintext under the current key. Returns
// (ciphertext, keyID, nil) or (plaintext, 0, nil) when encryption is
// disabled. Empty plaintext → (nil, 0, nil).
func Encrypt(plaintext []byte) ([]byte, byte, error) {
	if len(plaintext) == 0 {
		return nil, 0, nil
	}
	kr, err := loadKeyring()
	if err != nil {
		return nil, 0, err
	}
	if len(kr.keys) == 0 {
		// Disabled — pass through.
		return plaintext, 0, nil
	}
	key := kr.keys[kr.current]
	aead, err := gcmForKey(key)
	if err != nil {
		return nil, 0, err
	}
	nonce := make([]byte, gcmNonceLen)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, 0, err
	}
	sealed := aead.Seal(nil, nonce, plaintext, nil)
	out := make([]byte, 0, 1+gcmNonceLen+len(sealed))
	out = append(out, kr.current)
	out = append(out, nonce...)
	out = append(out, sealed...)
	return out, kr.current, nil
}

// Decrypt decrypts ciphertext using the key identified by its keyID byte
// header. keyID arg is the DB-stored column (sanity cross-check).
// Empty input → empty output, no error.
func Decrypt(ciphertext []byte, keyID byte) ([]byte, error) {
	if len(ciphertext) == 0 {
		return nil, nil
	}
	kr, err := loadKeyring()
	if err != nil {
		return nil, err
	}
	if len(kr.keys) == 0 {
		// Disabled — assume stored plaintext.
		return ciphertext, nil
	}
	if len(ciphertext) < 1+gcmNonceLen {
		// Legacy plaintext inserted when encryption was disabled.
		if keyID == 0 {
			return ciphertext, nil
		}
		return nil, errors.New("auth: ciphertext shorter than minimum (keyID + nonce)")
	}
	payloadKeyID := ciphertext[0]
	if keyID != 0 && payloadKeyID != keyID {
		return nil, fmt.Errorf("auth: keyID mismatch: payload=%d db=%d", payloadKeyID, keyID)
	}
	key, ok := kr.keys[payloadKeyID]
	if !ok {
		return nil, fmt.Errorf("auth: no key loaded for keyID %d — add it to HUE_ENC_KEYS", payloadKeyID)
	}
	aead, err := gcmForKey(key)
	if err != nil {
		return nil, err
	}
	nonce := ciphertext[1 : 1+gcmNonceLen]
	sealed := ciphertext[1+gcmNonceLen:]
	plain, err := aead.Open(nil, nonce, sealed, nil)
	if err != nil {
		return nil, fmt.Errorf("auth: decrypt (keyID %d): %w", payloadKeyID, err)
	}
	return plain, nil
}

// Reencrypt decrypts ciphertext with its embedded keyID then re-encrypts
// under the current key. Returns the new ciphertext + keyID. No-op
// (returns original) when the embedded key is already current.
func Reencrypt(ciphertext []byte, keyID byte) ([]byte, byte, error) {
	if len(ciphertext) == 0 {
		return nil, 0, nil
	}
	kr, err := loadKeyring()
	if err != nil {
		return nil, 0, err
	}
	if len(kr.keys) == 0 || len(ciphertext) < 1+gcmNonceLen {
		return ciphertext, keyID, nil // encryption disabled or plaintext passthrough
	}
	if ciphertext[0] == kr.current {
		return ciphertext, keyID, nil // already encrypted with current key
	}
	plain, err := Decrypt(ciphertext, keyID)
	if err != nil {
		return nil, 0, err
	}
	return Encrypt(plain)
}
