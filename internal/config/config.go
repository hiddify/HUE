// Package config loads HUE's configuration from environment variables.
//
// Convention: HUE_<KEY> for everything. envconfig also supports
// HUE_<KEY>_FILE for secret-file mounts (Docker/K8s) — set
// HUE_DB_URL_FILE=/run/secrets/db_url and the file's contents are read.
package config

import (
	"context"
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
	DatabaseURL  string `env:"HUE_DB_URL, required, file"`
	AutoMigrate  bool   `env:"HUE_AUTO_MIGRATE, default=false"`
	MaxOpenConns int    `env:"HUE_DB_MAX_OPEN_CONNS, default=20"`
	MaxIdleConns int    `env:"HUE_DB_MAX_IDLE_CONNS, default=10"`

	// Engine
	ConcurrentWindow time.Duration `env:"HUE_CONCURRENT_WINDOW, default=5m"`
	PenaltyDuration  time.Duration `env:"HUE_PENALTY_DURATION, default=10m"`

	// Geo
	MaxMindDBPath string `env:"HUE_MAXMIND_DB_PATH"`

	// Bootstrap
	BootstrapToken string `env:"HUE_BOOTSTRAP_TOKEN, file"`

	// Logging
	LogLevel  string `env:"HUE_LOG_LEVEL, default=info"`
	LogFormat string `env:"HUE_LOG_FORMAT, default=json"` // json | text

	// Shutdown
	ShutdownTimeout time.Duration `env:"HUE_SHUTDOWN_TIMEOUT, default=30s"`
}

// Load reads the environment into a Config and returns it.
func Load(ctx context.Context) (*Config, error) {
	var cfg Config
	if err := envconfig.Process(ctx, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}
