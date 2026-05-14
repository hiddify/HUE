// Package version implements HUE's single canonical version-encoding
// rule, used everywhere a SemVer-ish dotted string needs to become a
// monotone integer for comparison.
//
//   * Input: "25.3.7", "1.8.0", "25.07.01.123", ...
//   * Split on ".", pad to 4 parts with 0, each part 3 digits, each
//     part in [0, 999]. Parts > 999 → ErrPartTooLarge.
//   * Output: p1*1e9 + p2*1e6 + p3*1e3 + p4.
//
// Used in ConfigService.SyncConfig: an agent advertises its
// version=25.3.7, server picks the largest stored config key
// "xray.numeric_version.N" where N ≤ Encode("25.3.7").
package version

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// ErrPartTooLarge is returned when a version part exceeds 999.
var ErrPartTooLarge = errors.New("version: a part exceeds 999")

// Encode converts a dotted version string to its canonical monotone
// uint64. Empty string returns 0 (lowest), so "no version supplied"
// matches the lowest-N config.
func Encode(s string) (uint64, error) {
	if s == "" {
		return 0, nil
	}
	parts := strings.Split(s, ".")
	if len(parts) > 4 {
		return 0, fmt.Errorf("version: too many parts in %q (max 4)", s)
	}
	out := uint64(0)
	for i := 0; i < 4; i++ {
		var p uint64
		if i < len(parts) {
			n, err := strconv.ParseUint(strings.TrimSpace(parts[i]), 10, 64)
			if err != nil {
				return 0, fmt.Errorf("version: part %d %q: %w", i, parts[i], err)
			}
			if n > 999 {
				return 0, ErrPartTooLarge
			}
			p = n
		}
		switch i {
		case 0:
			out += p * 1_000_000_000
		case 1:
			out += p * 1_000_000
		case 2:
			out += p * 1_000
		case 3:
			out += p
		}
	}
	return out, nil
}

// MustEncode panics on error. Use for compile-time-known versions in
// tests only.
func MustEncode(s string) uint64 {
	v, err := Encode(s)
	if err != nil {
		panic(err)
	}
	return v
}

// PickHighestKey is the standard config-key picker. Given a map of
// keys like "xray.numeric_version.25003007000" and an encoded agent
// version, returns the key whose embedded numeric_version is the
// largest one ≤ agent. Returns "" when no key qualifies.
//
// Caller decides the prefix; this helper does the comparison so every
// adapter uses the same rule.
func PickHighestKey(keys []string, prefix string, agentEncoded uint64) string {
	best := ""
	bestN := uint64(0)
	first := true
	for _, k := range keys {
		if !strings.HasPrefix(k, prefix) {
			continue
		}
		rest := k[len(prefix):]
		n, err := strconv.ParseUint(rest, 10, 64)
		if err != nil {
			continue
		}
		if n > agentEncoded {
			continue
		}
		if first || n > bestN {
			best = k
			bestN = n
			first = false
		}
	}
	return best
}
