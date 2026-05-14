//go:build e2e

// Package e2e — end-to-end suite for HUE. Run with `make test-e2e`.
//
// This file owns shared bootstrap: a single PostgreSQL container is
// started per test binary via testcontainers-go; each test claims its
// own Postgres schema for isolation. HUE is constructed in-process
// (one bundle per test) reusing the same wiring as cmd/hue.
package e2e

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/test/bufconn"

	huev1 "github.com/hiddify/hue/gen/go/hue/v1"
	"github.com/hiddify/hue/internal/auth"
	"github.com/hiddify/hue/internal/cert"
	"github.com/hiddify/hue/internal/database"
	"github.com/hiddify/hue/internal/ent"
	"github.com/hiddify/hue/internal/eventstore"
	"github.com/hiddify/hue/internal/geo"
	"github.com/hiddify/hue/internal/server"
	"github.com/hiddify/hue/internal/service"
)

var (
	pgURL     string
	schemaSeq atomic.Uint64
)

func TestMain(m *testing.M) {
	ctx := context.Background()

	// Allow CI to inject an external Postgres so the suite skips the
	// testcontainers Docker dependency in environments that can't run it.
	if dsn := os.Getenv("HUE_E2E_DB_URL"); dsn != "" {
		pgURL = dsn
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

	_ = os.Setenv("HUE_PASSWORD_ENC_KEY",
		"deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef")

	os.Exit(m.Run())
}

func uniqueSchemaName() string {
	return fmt.Sprintf("hue_e2e_%d", schemaSeq.Add(1))
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

// hueEnv is the in-process HUE handle every E2E test grabs. Same shape
// as internal/server/integration's testEnv with an Agent client added.
type hueEnv struct {
	DB         *ent.Client
	Owner      string // plaintext Owner API key
	Engine     *service.Engine

	Admin           huev1.AdminServiceClient
	Auth            huev1.AuthServiceClient
	AuthAdmin       huev1.AuthAdminServiceClient
	Certs           huev1.DomainCertificateServiceClient
	Config          huev1.ConfigServiceClient
	ResellerClients huev1.ResellerClientServiceClient
	Resellers       huev1.ResellerManagementServiceClient
	Usage           huev1.UsageServiceClient

	conn *grpc.ClientConn
}

func newHueEnv(t *testing.T) *hueEnv {
	t.Helper()
	ctx := context.Background()

	schema := uniqueSchemaName()
	t.Cleanup(createSchema(t, schema))

	dsn := pgURL
	if strings.Contains(dsn, "?") {
		dsn += "&search_path=" + schema
	} else {
		dsn += "?search_path=" + schema
	}

	db, err := database.Open(ctx, database.Config{
		DSN:          dsn,
		MaxOpenConns: 5,
		MaxIdleConns: 2,
		AutoMigrate:  true,
	})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	_, owner, _, err := auth.GenerateKey(auth.KindOwner)
	if err != nil {
		t.Fatalf("generate owner key: %v", err)
	}
	if err := auth.Bootstrap(ctx, db, owner, silentLogger()); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	signingKey, err := auth.EnsureSigningKey(ctx, db)
	if err != nil {
		t.Fatalf("signing key: %v", err)
	}

	locks := service.NewLockManager()
	sessions := service.NewSessionTracker(5 * time.Minute)
	penalties := service.NewPenaltyTracker(10 * time.Minute)
	resellers := service.NewResellerHierarchy(db, locks)
	events := eventstore.New(db, silentLogger())
	geoR := &geo.Resolver{}
	engine := service.NewEngine(service.EngineDeps{
		DB: db, Geo: geoR, Locks: locks,
		Sessions: sessions, Penalties: penalties, Resellers: resellers,
		Events: events,
	})

	authn := auth.NewAuthenticator(db)
	bundle := server.New(server.Deps{
		DB:             db,
		Engine:         engine,
		Auth:           authn,
		Resellers:      resellers,
		SigningKey:     signingKey,
		Lockout:        auth.NewLockout(0, 0),
		Events:         events,
		Logger:         silentLogger(),
		ACMEChallenger: cert.NewHTTP01Challenger(),
	})

	grpcSrv := grpc.NewServer(
		grpc.ChainUnaryInterceptor(
			server.PanicRecovery(silentLogger()),
			authn.UnaryInterceptor(),
			server.AuthorizeUnary(),
		),
	)
	bundle.Register(grpcSrv)
	bundle.MarkServing()

	bufLis := bufconn.Listen(1 << 20)
	t.Cleanup(func() { _ = bufLis.Close() })
	go func() { _ = grpcSrv.Serve(bufLis) }()
	t.Cleanup(grpcSrv.GracefulStop)

	conn, err := grpc.NewClient(
		"passthrough://buf",
		grpc.WithContextDialer(func(_ context.Context, _ string) (net.Conn, error) { return bufLis.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("gateway dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	return &hueEnv{
		DB:              db,
		Owner:           owner,
		Engine:          engine,
		Admin:           huev1.NewAdminServiceClient(conn),
		Auth:            huev1.NewAuthServiceClient(conn),
		AuthAdmin:       huev1.NewAuthAdminServiceClient(conn),
		Certs:           huev1.NewDomainCertificateServiceClient(conn),
		Config:          huev1.NewConfigServiceClient(conn),
		ResellerClients: huev1.NewResellerClientServiceClient(conn),
		Resellers:       huev1.NewResellerManagementServiceClient(conn),
		Usage:           huev1.NewUsageServiceClient(conn),
		conn:            conn,
	}
}

func (e *hueEnv) ownerCtx() context.Context {
	return metadata.AppendToOutgoingContext(context.Background(),
		"authorization", "Bearer "+e.Owner)
}

func (e *hueEnv) tokenCtx(tok string) context.Context {
	return metadata.AppendToOutgoingContext(context.Background(),
		"authorization", "Bearer "+tok)
}
