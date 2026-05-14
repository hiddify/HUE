//go:build integration

// Package integration runs HUE's gRPC server against a real PostgreSQL
// container managed by testcontainers-go. One container is shared across
// every test in this binary; per-test isolation = its own Postgres
// schema (CREATE SCHEMA / DROP SCHEMA CASCADE on cleanup).
package integration

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sync/atomic"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

var (
	pgURL     string
	schemaSeq atomic.Uint64
)

func TestMain(m *testing.M) {
	ctx := context.Background()

	// Allow CI override.
	if dsn := os.Getenv("HUE_TEST_DB_URL"); dsn != "" {
		pgURL = dsn
		// Force AES-GCM encryption for tests so the encryption path is exercised.
		_ = os.Setenv("HUE_PASSWORD_ENC_KEY",
			"deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef")
		os.Exit(m.Run())
	}

	c, err := tcpostgres.Run(ctx,
		"postgres:16-alpine",
		tcpostgres.WithDatabase("hue"),
		tcpostgres.WithUsername("hue"),
		tcpostgres.WithPassword("hue"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(60*time.Second),
		),
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "start postgres: %v\n", err)
		os.Exit(1)
	}
	defer func() { _ = c.Terminate(ctx) }()

	pgURL, err = c.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		fmt.Fprintf(os.Stderr, "get dsn: %v\n", err)
		os.Exit(1)
	}

	// Force AES-GCM encryption for tests so the encryption path is
	// exercised throughout (instead of pass-through).
	_ = os.Setenv("HUE_PASSWORD_ENC_KEY",
		"deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef")

	os.Exit(m.Run())
}

func uniqueSchemaName() string {
	return fmt.Sprintf("hue_test_%d", schemaSeq.Add(1))
}

func createSchema(t *testing.T, name string) func() {
	t.Helper()
	db, err := sql.Open("pgx", pgURL)
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	defer db.Close()
	if _, err := db.ExecContext(context.Background(), fmt.Sprintf(`CREATE SCHEMA "%s"`, name)); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	return func() {
		db, err := sql.Open("pgx", pgURL)
		if err != nil {
			return
		}
		defer db.Close()
		_, _ = db.ExecContext(context.Background(),
			fmt.Sprintf(`DROP SCHEMA "%s" CASCADE`, name))
	}
}

func silentLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}
