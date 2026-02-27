package config

import (
	"os"
	"strings"
	"time"

	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/env"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/v2"
)

// Config holds all application configuration
type Config struct {
	// Core & Database
	DatabaseURL string `koanf:"db_url"`
	Port        string `koanf:"port"`
	LogLevel    string `koanf:"log_level"`
	LogFile     string `koanf:"log_file"`

	// Performance & Quota Engine
	ReportInterval      time.Duration `koanf:"report_interval"`
	DBFlushInterval     time.Duration `koanf:"db_flush_interval"`
	DisconnectBatchSize int           `koanf:"disconnect_batch_size"`
	UsageDataRetention  time.Duration `koanf:"usage_data_retention"`
	HistDataRetention   time.Duration `koanf:"hist_data_retention"`

	// Concurrent & Penalty Logic
	ConcurrentWindow time.Duration `koanf:"concurrent_window"`
	PenaltyDuration  time.Duration `koanf:"penalty_duration"`

	// Geo-IP & Privacy
	MaxMindDBPath string `koanf:"maxmind_db_path"`

	// Security
	AuthSecret     string   `koanf:"auth_secret"`
	TLSCertPath    string   `koanf:"tls_cert"`
	TLSKeyPath     string   `koanf:"tls_key"`
	AllowedNodeIPs []string `koanf:"allowed_node_ips"`

	// Event Sourcing
	EventStoreType string `koanf:"event_store_type"`

	// HTTP Port (derived)
	HTTPPort string
}

// defaults returns default configuration values
func defaults() Config {
	return Config{
		DatabaseURL:         "sqlite://./hue.db",
		Port:                "50051",
		HTTPPort:            "50052",
		LogLevel:            "info",
		LogFile:             "",
		ReportInterval:      60 * time.Second,
		DBFlushInterval:     5 * time.Minute,
		DisconnectBatchSize: 50,
		UsageDataRetention:  30 * 24 * time.Hour,
		HistDataRetention:   365 * 24 * time.Hour,
		ConcurrentWindow:    5 * time.Minute,
		PenaltyDuration:     10 * time.Minute,
		MaxMindDBPath:       "",
		AuthSecret:          "",
		TLSCertPath:         "",
		TLSKeyPath:          "",
		AllowedNodeIPs:      []string{},
		EventStoreType:      "db",
	}
}

// Load reads configuration from environment variables and optional config file
func Load() (*Config, error) {
	k := koanf.New(".")

	// Set defaults
	cfg := defaults()

	// Try to load from config file (optional)
	if _, err := os.Stat("config.yaml"); err == nil {
		if err := k.Load(file.Provider("config.yaml"), yaml.Parser()); err != nil {
			return nil, err
		}
	}

	// Load from environment variables with HUE_ prefix.
	// We use "." as the koanf delimiter here so that underscores in the key
	// name are preserved as-is (e.g. HUE_AUTH_SECRET → auth_secret, not
	// split into a nested path auth.secret).
	if err := k.Load(env.Provider("HUE_", ".", func(s string) string {
		return strings.ToLower(strings.TrimPrefix(s, "HUE_"))
	}), nil); err != nil {
		return nil, err
	}

	// Unmarshal into config struct
	if err := k.Unmarshal("", &cfg); err != nil {
		return nil, err
	}

	// Set HTTP port (gRPC port + 1 if not explicitly set)
	cfg.HTTPPort = "50052"
	if cfg.Port != "50051" {
		// If custom gRPC port, calculate HTTP port
		cfg.HTTPPort = "50052"
	}

	return &cfg, nil
}
