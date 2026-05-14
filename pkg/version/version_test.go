package version

import (
	"strings"
	"testing"
)

func TestEncode_BasicShapes(t *testing.T) {
	t.Parallel()
	cases := map[string]uint64{
		"":                0,
		"0":               0,
		"1":               1_000_000_000,
		"1.8":             1_008_000_000,
		"1.8.0":           1_008_000_000,
		"25.3.7":          25_003_007_000,
		"25.07.01.123":    25_007_001_123,
		"999.999.999.999": 999_999_999_999,
	}
	for s, want := range cases {
		got, err := Encode(s)
		if err != nil {
			t.Fatalf("Encode(%q): %v", s, err)
		}
		if got != want {
			t.Errorf("Encode(%q) = %d, want %d", s, got, want)
		}
	}
}

func TestEncode_Monotone(t *testing.T) {
	t.Parallel()
	a, _ := Encode("25.3.7")
	b, _ := Encode("25.3.8")
	c, _ := Encode("25.4.0")
	d, _ := Encode("26.0.0")
	if !(a < b && b < c && c < d) {
		t.Errorf("not monotone: %d < %d < %d < %d", a, b, c, d)
	}
}

func TestEncode_RejectsOversizedPart(t *testing.T) {
	t.Parallel()
	if _, err := Encode("1000.0.0"); err == nil {
		t.Fatal("expected ErrPartTooLarge")
	}
	if _, err := Encode("0.0.0.0.0"); err == nil {
		t.Fatal("expected too-many-parts error")
	}
	if _, err := Encode("not-a-version"); err == nil {
		t.Fatal("expected parse error")
	}
}

func TestPickHighestKey(t *testing.T) {
	t.Parallel()
	keys := []string{
		"xray.numeric_version.0",
		"xray.numeric_version.25003007000",
		"xray.numeric_version.25006001000",
		"wireguard.numeric_version.1000000000",
		"unrelated.key",
	}
	agent, _ := Encode("25.5.0")
	best := PickHighestKey(keys, "xray.numeric_version.", agent)
	if best != "xray.numeric_version.25003007000" {
		t.Errorf("got %q; expected the 25.3.7 key (largest ≤ 25.5.0)", best)
	}

	// Agent too old → falls back to the .0 bucket.
	old, _ := Encode("20.0.0")
	got := PickHighestKey(keys, "xray.numeric_version.", old)
	if got != "xray.numeric_version.0" {
		t.Errorf("got %q; expected the fallback .0 key for old agents", got)
	}

	// Wrong prefix → empty.
	none := PickHighestKey(keys, "missing.", agent)
	if none != "" {
		t.Errorf("expected empty for unknown prefix, got %q", none)
	}

	// Agent fresher than any stored version → still picks the newest
	// available (largest ≤ agent).
	fresh, _ := Encode("999.999.999")
	got = PickHighestKey(keys, "xray.numeric_version.", fresh)
	if !strings.HasPrefix(got, "xray.numeric_version.") {
		t.Errorf("got %q; expected an xray.* key", got)
	}
}
