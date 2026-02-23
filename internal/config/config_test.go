package config

import (
	"testing"
	"time"
)

func TestLoadConfigDefaults(t *testing.T) {
	cfg := defaults()

	if cfg.DatabaseURL != "sqlite://./hue.db" {
		t.Fatalf("unexpected default db url: %s", cfg.DatabaseURL)
	}
	if cfg.Port != "50051" {
		t.Fatalf("unexpected default port: %s", cfg.Port)
	}
	if cfg.HTTPPort != "50052" {
		t.Fatalf("unexpected default http port: %s", cfg.HTTPPort)
	}
	if cfg.DBFlushInterval != 5*time.Minute {
		t.Fatalf("unexpected default flush interval: %v", cfg.DBFlushInterval)
	}
}

func TestLoadConfigEnvOverride(t *testing.T) {
	t.Setenv("HUE_AUTH_SECRET", "super-secret")
	t.Setenv("HUE_PORT", "60051")
	t.Setenv("HUE_DB_URL", "sqlite://./custom.db")
	t.Setenv("HUE_DB_FLUSH_INTERVAL", "30s")
	t.Setenv("HUE_CONCURRENT_WINDOW", "90s")
	t.Setenv("HUE_ALLOWED_NODE_IPS", "10.0.0.0/8,127.0.0.1")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	if cfg.AuthSecret != "super-secret" {
		t.Fatalf("expected auth secret override")
	}
	if cfg.Port != "60051" {
		t.Fatalf("expected port override, got %s", cfg.Port)
	}
	if cfg.DatabaseURL != "sqlite://./custom.db" {
		t.Fatalf("expected db override, got %s", cfg.DatabaseURL)
	}
	if cfg.DBFlushInterval != 30*time.Second {
		t.Fatalf("expected flush override, got %v", cfg.DBFlushInterval)
	}
	if cfg.ConcurrentWindow != 90*time.Second {
		t.Fatalf("expected concurrent window override, got %v", cfg.ConcurrentWindow)
	}
}
