package template_test

import (
	"context"
	"errors"
	"testing"

	"github.com/hiddify/hue/pkg/agents"
	"github.com/hiddify/hue/pkg/agents/template"
)

// Compile-time assertion that the template satisfies agents.Agent.
// Every adapter package should ship this single line.
var _ agents.Agent = (*template.Client)(nil)

// TestTemplate_AllMethodsAreUnsupported is the contract for an adapter
// with Capabilities() == 0: every method must return ErrUnsupported
// (or, for Close, succeed). New adapters that implement methods will
// shrink this expectation as bits are turned on in Capabilities.
func TestTemplate_AllMethodsAreUnsupported(t *testing.T) {
	t.Parallel()
	c, err := template.New(template.Config{Endpoint: "ignored:0"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	if got := c.Name(); got != "template" {
		t.Errorf("Name() = %q, want %q", got, "template")
	}
	if got := c.Capabilities(); got != 0 {
		t.Errorf("Capabilities() = %v, want 0 for the empty template", got)
	}

	ctx := context.Background()
	checks := []struct {
		name string
		fn   func() error
	}{
		{"Healthcheck", func() error { return c.Healthcheck(ctx) }},
		{"ReadStats", func() error { _, err := c.ReadStats(ctx); return err }},
		{"Disconnect", func() error { return c.Disconnect(ctx, agents.User{ID: "u"}) }},
		{"AddUser", func() error { return c.AddUser(ctx, agents.User{ID: "u"}, "s") }},
		{"RemoveUser", func() error { return c.RemoveUser(ctx, agents.User{ID: "u"}) }},
		{"SyncConfig", func() error { _, err := c.SyncConfig(ctx); return err }},
	}
	for _, ch := range checks {
		if err := ch.fn(); !errors.Is(err, agents.ErrUnsupported) {
			t.Errorf("%s: got %v, want errors.Is(err, ErrUnsupported)", ch.name, err)
		}
	}
}

func TestTemplate_NewRejectsEmptyEndpoint(t *testing.T) {
	t.Parallel()
	if _, err := template.New(template.Config{}); err == nil {
		t.Error("New must reject an empty Endpoint")
	}
}
