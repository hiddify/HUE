package auth

import (
	"bytes"
	"testing"
)

// 32 zero bytes, hex-encoded — deterministic test keys.
const (
	key1Hex = "0000000000000000000000000000000000000000000000000000000000000001"
	key2Hex = "0000000000000000000000000000000000000000000000000000000000000002"
)

func withEnv(t *testing.T, kv ...string) {
	t.Helper()
	if len(kv)%2 != 0 {
		t.Fatal("withEnv: odd kv args")
	}
	for i := 0; i < len(kv); i += 2 {
		t.Setenv(kv[i], kv[i+1])
	}
}

func TestEncrypt_RoundTrip_SingleKey(t *testing.T) {
	withEnv(t, "HUE_PASSWORD_ENC_KEY", key1Hex)
	plain := []byte("hunter2")
	ct, kid, err := Encrypt(plain)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if kid != 1 {
		t.Errorf("keyID = %d, want 1", kid)
	}
	got, err := Decrypt(ct, kid)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if !bytes.Equal(got, plain) {
		t.Errorf("Decrypt = %q, want %q", got, plain)
	}
}

func TestEncrypt_Disabled_Passthrough(t *testing.T) {
	withEnv(t, "HUE_PASSWORD_ENC_KEY", "", "HUE_ENC_KEYS", "")
	plain := []byte("pass")
	ct, kid, err := Encrypt(plain)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if kid != 0 {
		t.Errorf("disabled keyID = %d, want 0", kid)
	}
	if !bytes.Equal(ct, plain) {
		t.Errorf("disabled ciphertext should be plaintext")
	}
	got, err := Decrypt(ct, 0)
	if err != nil {
		t.Fatalf("Decrypt disabled: %v", err)
	}
	if !bytes.Equal(got, plain) {
		t.Errorf("disabled Decrypt = %q, want %q", got, plain)
	}
}

func TestEncrypt_EmptyPlaintext(t *testing.T) {
	withEnv(t, "HUE_PASSWORD_ENC_KEY", key1Hex)
	ct, kid, err := Encrypt(nil)
	if err != nil || ct != nil || kid != 0 {
		t.Errorf("empty Encrypt: ct=%v kid=%d err=%v, want nil,0,nil", ct, kid, err)
	}
	got, err := Decrypt(nil, 0)
	if err != nil || got != nil {
		t.Errorf("empty Decrypt: got=%v err=%v, want nil,nil", got, err)
	}
}

func TestEncrypt_MultiKey_CurrentIsHighest(t *testing.T) {
	withEnv(t,
		"HUE_ENC_KEYS", "1:"+key1Hex+",2:"+key2Hex,
		"HUE_ENC_KEY_CURRENT", "2",
		"HUE_PASSWORD_ENC_KEY", "",
	)
	plain := []byte("secret")
	ct, kid, err := Encrypt(plain)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if kid != 2 {
		t.Errorf("keyID = %d, want 2 (current)", kid)
	}
	if ct[0] != 2 {
		t.Errorf("ciphertext[0] = %d, want 2", ct[0])
	}
	// Decrypt using multi-key ring (should pick key2).
	got, err := Decrypt(ct, kid)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if !bytes.Equal(got, plain) {
		t.Errorf("Decrypt = %q, want %q", got, plain)
	}
}

func TestDecrypt_OldKeyAfterRotation(t *testing.T) {
	// Encrypt with key1, then decrypt after adding key2 as current.

	// Phase 1: encrypt with key1.
	withEnv(t, "HUE_PASSWORD_ENC_KEY", key1Hex, "HUE_ENC_KEYS", "", "HUE_ENC_KEY_CURRENT", "")
	plain := []byte("oldpassword")
	ct, kid, err := Encrypt(plain)
	if err != nil {
		t.Fatalf("Encrypt (key1): %v", err)
	}
	if kid != 1 {
		t.Fatalf("expected keyID 1, got %d", kid)
	}

	// Phase 2: add key2 as current; keep key1 for decryption of old data.
	withEnv(t,
		"HUE_ENC_KEYS", "1:"+key1Hex+",2:"+key2Hex,
		"HUE_ENC_KEY_CURRENT", "2",
		"HUE_PASSWORD_ENC_KEY", "",
	)
	got, err := Decrypt(ct, kid) // should pick key1 from ciphertext header
	if err != nil {
		t.Fatalf("Decrypt after rotation: %v", err)
	}
	if !bytes.Equal(got, plain) {
		t.Errorf("Decrypt = %q, want %q", got, plain)
	}
}

func TestReencrypt_MigratesOldKey(t *testing.T) {

	// Encrypt with key1 via legacy path.
	withEnv(t, "HUE_PASSWORD_ENC_KEY", key1Hex, "HUE_ENC_KEYS", "", "HUE_ENC_KEY_CURRENT", "")
	plain := []byte("migrate_me")
	old, oldKID, err := Encrypt(plain)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}

	// Rotate to key2.
	withEnv(t,
		"HUE_ENC_KEYS", "1:"+key1Hex+",2:"+key2Hex,
		"HUE_ENC_KEY_CURRENT", "2",
		"HUE_PASSWORD_ENC_KEY", "",
	)
	newCT, newKID, err := Reencrypt(old, oldKID)
	if err != nil {
		t.Fatalf("Reencrypt: %v", err)
	}
	if newKID != 2 {
		t.Errorf("Reencrypt keyID = %d, want 2", newKID)
	}
	if newCT[0] != 2 {
		t.Errorf("Reencrypt ciphertext[0] = %d, want 2", newCT[0])
	}
	got, err := Decrypt(newCT, newKID)
	if err != nil {
		t.Fatalf("Decrypt reencrypted: %v", err)
	}
	if !bytes.Equal(got, plain) {
		t.Errorf("Decrypt = %q, want %q", got, plain)
	}
}

func TestReencrypt_NoopWhenCurrentKey(t *testing.T) {
	withEnv(t,
		"HUE_ENC_KEYS", "1:"+key1Hex+",2:"+key2Hex,
		"HUE_ENC_KEY_CURRENT", "2",
		"HUE_PASSWORD_ENC_KEY", "",
	)
	plain := []byte("already_current")
	ct, kid, _ := Encrypt(plain)
	newCT, newKID, err := Reencrypt(ct, kid)
	if err != nil {
		t.Fatalf("Reencrypt: %v", err)
	}
	if !bytes.Equal(newCT, ct) || newKID != kid {
		t.Errorf("Reencrypt changed ciphertext when already current — expected no-op")
	}
}

func TestDecrypt_MissingKey_Error(t *testing.T) {
	// Encrypt with key1.
	withEnv(t, "HUE_PASSWORD_ENC_KEY", key1Hex, "HUE_ENC_KEYS", "", "HUE_ENC_KEY_CURRENT", "")
	ct, kid, _ := Encrypt([]byte("x"))

	// Now only expose key2 — key1 not loaded.
	withEnv(t,
		"HUE_ENC_KEYS", "2:"+key2Hex,
		"HUE_ENC_KEY_CURRENT", "2",
		"HUE_PASSWORD_ENC_KEY", "",
	)
	_, err := Decrypt(ct, kid)
	if err == nil {
		t.Fatal("Decrypt with missing key: expected error, got nil")
	}
}

func TestParseKeyring_InvalidFormats(t *testing.T) {
	cases := []struct{ name, raw, current string }{
		{"no colon", "1" + key1Hex, ""},
		{"keyID zero", "0:" + key1Hex, ""},
		{"bad hex", "1:notvalidhex0000000000000000000000000000000000000000000000000000", ""},
		{"current not in keys", "1:" + key1Hex, "2"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseKeyring(tc.raw, tc.current)
			if err == nil {
				t.Errorf("%s: expected error, got nil", tc.name)
			}
		})
	}
}
