// Package config loads HUE's configuration from environment variables.
//
// Convention: HUE_<KEY> for everything. For Docker/K8s secret-file
// mounts, set HUE_<KEY>_FILE=/path/to/secret and we read its contents
// in place of HUE_<KEY>.
package config

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/sethvargo/go-envconfig"
)

// Config is the single source of truth for runtime settings.
type Config struct {
	// Network
	Addr string `env:"HUE_ADDR, default=:8443"`

	// TLS — when both files are set, the listener serves TLS. Otherwise
	// h2c (cleartext HTTP/2) is used; intended for dev and behind a TLS
	// terminator only.
	TLSCertFile string `env:"HUE_TLS_CERT"`
	TLSKeyFile  string `env:"HUE_TLS_KEY"`

	// Database
	DatabaseURL  string `env:"HUE_DB_URL, required"`
	AutoMigrate  bool   `env:"HUE_AUTO_MIGRATE, default=false"`
	MaxOpenConns int    `env:"HUE_DB_MAX_OPEN_CONNS, default=20"`
	MaxIdleConns int    `env:"HUE_DB_MAX_IDLE_CONNS, default=10"`

	// Engine
	ConcurrentWindow time.Duration `env:"HUE_CONCURRENT_WINDOW, default=5m"`
	PenaltyDuration  time.Duration `env:"HUE_PENALTY_DURATION, default=10m"`

	// Geo
	MaxMindDBPath string `env:"HUE_MAXMIND_DB_PATH"`

	// Bootstrap
	BootstrapToken string `env:"HUE_BOOTSTRAP_TOKEN"`

	// Logging
	LogLevel  string `env:"HUE_LOG_LEVEL, default=info"`
	LogFormat string `env:"HUE_LOG_FORMAT, default=json"` // json | text

	// Shutdown
	ShutdownTimeout time.Duration `env:"HUE_SHUTDOWN_TIMEOUT, default=30s"`
}

// Load reads the environment into a Config and returns it.
//
// For any HUE_<KEY> we also accept HUE_<KEY>_FILE pointing at a
// secret-file mount (Docker/Kubernetes). When both are set, the file
// wins, so manifests can keep a single mounted secret-path env var.
func Load(ctx context.Context) (*Config, error) {
	if err := expandFileEnvs(); err != nil {
		return nil, err
	}
	var cfg Config
	if err := envconfig.Process(ctx, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// expandFileEnvs walks os.Environ() looking for HUE_*_FILE entries and,
// for each, reads the file and sets HUE_<KEY>. Existing HUE_<KEY> values
// are overwritten — the file is the more specific signal.
func expandFileEnvs() error {
	for _, kv := range os.Environ() {
		eq := strings.IndexByte(kv, '=')
		if eq < 0 {
			continue
		}
		key, path := kv[:eq], kv[eq+1:]
		if !strings.HasPrefix(key, "HUE_") || !strings.HasSuffix(key, "_FILE") || path == "" {
			continue
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("config: read %s: %w", key, err)
		}
		target := strings.TrimSuffix(key, "_FILE")
		_ = os.Setenv(target, strings.TrimRight(string(raw), "\r\n "))
	}
	return nil
}
