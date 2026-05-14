package agents

import "testing"

func TestCapabilityHas(t *testing.T) {
	t.Parallel()
	caps := CapHealthcheck | CapStats
	if !caps.Has(CapHealthcheck) {
		t.Error("Has(CapHealthcheck) must be true when CapHealthcheck is set")
	}
	if !caps.Has(CapStats) {
		t.Error("Has(CapStats) must be true when CapStats is set")
	}
	if caps.Has(CapDisconnect) {
		t.Error("Has(CapDisconnect) must be false when bit is not set")
	}
	// Composite check: requiring all bits in a multi-bit mask.
	if !caps.Has(CapHealthcheck | CapStats) {
		t.Error("Has(multi-bit) must require ALL bits, not any")
	}
	if caps.Has(CapHealthcheck | CapDisconnect) {
		t.Error("Has(multi-bit) must reject when any bit is missing")
	}
}
