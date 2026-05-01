// Package database opens the ent client against PostgreSQL and applies
// schema migrations.
//
// Migration policy (current iteration):
//   - HUE_AUTO_MIGRATE=true → call ent.Schema.Create on startup. Suitable for
//     dev and first-deploy. ent generates idempotent CREATE-IF-NOT-EXISTS DDL.
//   - HUE_AUTO_MIGRATE=false → migrations are applied out-of-band (planned:
//     Atlas-versioned SQL files under internal/ent/migrate/migrations/, run
//     by `migrate up` before the binary starts).
//
// Partitioning of events and usage_reports is not yet implemented — those
// tables ship as regular tables for now. Adding declarative range
// partitioning by `ts` is tracked under Phase 8 of the rewrite plan.
package database

import (
	"context"
	"database/sql"
	"fmt"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	_ "github.com/jackc/pgx/v5/stdlib" // pgx driver registers as "pgx"

	"github.com/hiddify/hue/internal/ent"
	"github.com/hiddify/hue/internal/ent/migrate"
)

// Config carries the database wiring options. All fields except DSN have
// sensible defaults; pass a zero value to take them.
type Config struct {
	DSN             string
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime int // seconds; 0 = forever
	AutoMigrate     bool
}

// Open establishes a pgx-backed *sql.DB and wraps it in an ent client.
func Open(ctx context.Context, cfg Config) (*ent.Client, error) {
	if cfg.DSN == "" {
		return nil, fmt.Errorf("database: empty DSN")
	}
	db, err := sql.Open("pgx", cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("database: open: %w", err)
	}
	if cfg.MaxOpenConns > 0 {
		db.SetMaxOpenConns(cfg.MaxOpenConns)
	}
	if cfg.MaxIdleConns > 0 {
		db.SetMaxIdleConns(cfg.MaxIdleConns)
	}
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("database: ping: %w", err)
	}
	drv := entsql.OpenDB(dialect.Postgres, db)
	client := ent.NewClient(ent.Driver(drv))

	if cfg.AutoMigrate {
		if err := client.Schema.Create(
			ctx,
			migrate.WithDropIndex(false),
			migrate.WithDropColumn(false),
		); err != nil {
			_ = client.Close()
			return nil, fmt.Errorf("database: schema create: %w", err)
		}
	}
	return client, nil
}
