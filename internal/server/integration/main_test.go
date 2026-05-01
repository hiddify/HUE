//go:build integration

// Package integration runs HUE's gRPC server against a real PostgreSQL
// container managed by testcontainers-go. One container is shared across
// every test in this binary; per-test isolation is achieved by giving
// each test its own Postgres schema (CREATE SCHEMA → set search_path →
// DROP SCHEMA on cleanup). That's substantially faster than starting a
// container per test and still correct because schemas are independent.
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
	pgURL    string
	schemaSeq atomic.Uint64
)

func TestMain(m *testing.M) {
	ctx := context.Background()

	// Allow CI / dev overrides to skip the container — useful when a
	// long-running Postgres is already there and you just want to
	// iterate fast on tests.
	if dsn := os.Getenv("HUE_TEST_DB_URL"); dsn != "" {
		pgURL = dsn
		os.Exit(m.Run())
	}

	c, err := tcpostgres.Run(ctx,
		"postgres:16-alpine",
		tcpostgres.WithDatabase("hue"),
		tcpostgres.WithUsername("hue"),
		tcpostgres.WithPassword("hue"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2). // first one is during initdb; we need the runtime listener
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

	os.Exit(m.Run())
}

// uniqueSchemaName returns a fresh, valid Postgres identifier per call.
func uniqueSchemaName() string {
	return fmt.Sprintf("hue_test_%d", schemaSeq.Add(1))
}

// createSchema opens a short-lived raw connection and provisions the
// schema. Returns a function that drops it on cleanup.
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

// silentLogger is the default for tests — keeps output clean unless a
// test explicitly wants to see logs (then build with `-v` and pass an
// info-level handler in the local helper).
func silentLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}
