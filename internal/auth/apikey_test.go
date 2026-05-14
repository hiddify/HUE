package auth

import (
	"strings"
	"testing"
)

func TestGenerateKey_Roundtrip(t *testing.T) {
	t.Parallel()
	for _, kind := range []ActorKind{KindOwner, KindAgent} {
		t.Run(string(kind), func(t *testing.T) {
			t.Parallel()
			prefix, plaintext, hash, err := GenerateKey(kind)
			if err != nil {
				t.Fatalf("GenerateKey: %v", err)
			}
			expectPfx, _ := PrefixForKind(kind)
			if !strings.HasPrefix(plaintext, expectPfx+"_") {
				t.Fatalf("plaintext does not start with kind prefix: %s", plaintext)
			}
			if !strings.HasPrefix(prefix, expectPfx+"_") || len(prefix) != len(expectPfx)+1+prefixBodyChrs {
				t.Fatalf("lookup prefix shape unexpected: %s (len %d)", prefix, len(prefix))
			}
			if got := LookupPrefix(plaintext); got != prefix {
				t.Fatalf("LookupPrefix(plaintext) = %s, want %s", got, prefix)
			}
			if err := VerifyToken(plaintext, hash); err != nil {
				t.Fatalf("VerifyToken on freshly generated token failed: %v", err)
			}
		})
	}
}

func TestVerifyToken_RejectsTampered(t *testing.T) {
	t.Parallel()
	_, plaintext, hash, err := GenerateKey(KindOwner)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	tampered := plaintext[:len(plaintext)-1] + "x"
	if err := VerifyToken(tampered, hash); err == nil {
		t.Fatal("VerifyToken accepted a tampered token")
	}
}

func TestVerifyToken_RejectsMalformedHash(t *testing.T) {
	t.Parallel()
	cases := []string{
		"",
		"$argon2id$v=19$m=65536,t=3,p=4$abc$def",        // wrong base64
		"$argon2i$v=19$m=65536,t=3,p=4$YWFh$YWFh",        // wrong algo
		"$argon2id$v=99$m=65536,t=3,p=4$YWFh$YWFh",       // wrong version
		"$argon2id$v=19$m=foo,t=3,p=4$YWFh$YWFh",         // bad params
		"not-a-phc-string",
	}
	for _, c := range cases {
		c := c
		t.Run("malformed/"+c, func(t *testing.T) {
			t.Parallel()
			if err := VerifyToken("anything", c); err == nil {
				t.Fatalf("expected error for malformed hash %q", c)
			}
		})
	}
}

func TestLookupPrefix(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"own_abcdefgh1234":   "own_abcdefgh",
		"agt_aaaaaaaabbbbcc": "agt_aaaaaaaa",
		"":                   "",
		"noprefix":           "",
		"own_short":          "", // body shorter than prefixBodyChrs
	}
	for in, want := range cases {
		if got := LookupPrefix(in); got != want {
			t.Errorf("LookupPrefix(%q) = %q, want %q", in, got, want)
		}
	}
}
