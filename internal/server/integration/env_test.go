//go:build integration

package integration

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/test/bufconn"

	huev1 "github.com/hiddify/hue/gen/go/hue/v1"
	"github.com/hiddify/hue/internal/auth"
	"github.com/hiddify/hue/internal/database"
	"github.com/hiddify/hue/internal/ent"
	"github.com/hiddify/hue/internal/eventstore"
	"github.com/hiddify/hue/internal/geo"
	"github.com/hiddify/hue/internal/server"
	"github.com/hiddify/hue/internal/service"
)

// testEnv holds everything a test needs: typed gRPC clients, the
// underlying ent client (for direct DB assertions), the engine (for
// white-box manipulations), and a manager bootstrap token.
type testEnv struct {
	DB      *ent.Client
	Engine  *service.Engine
	Admin   huev1.AdminServiceClient
	Usage   huev1.UsageServiceClient
	Locks   *service.LockManager
	Penalty *service.PenaltyTracker

	// Plaintext manager token issued by Bootstrap. Use authCtx to wrap
	// outgoing contexts.
	Token string
}

// newTestEnv builds a fully wired server backed by a fresh schema. Every
// resource cleans up via t.Cleanup.
func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	ctx := context.Background()

	schema := uniqueSchemaName()
	dropSchema := createSchema(t, schema)
	t.Cleanup(dropSchema)

	dsn := pgURL
	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	dsn += sep + "search_path=" + schema

	db, err := database.Open(ctx, database.Config{
		DSN:          dsn,
		MaxOpenConns: 5,
		MaxIdleConns: 2,
		AutoMigrate:  true,
	})
	require.NoError(t, err, "database.Open")
	t.Cleanup(func() { _ = db.Close() })

	// Issue a fresh manager bootstrap token per env.
	_, plaintext, _, err := auth.GenerateKey(auth.KindManager)
	require.NoError(t, err)
	require.NoError(t, auth.Bootstrap(ctx, db, plaintext, silentLogger()))

	// Engine deps
	locks := service.NewLockManager()
	sessions := service.NewSessionTracker(5 * time.Minute)
	penalties := service.NewPenaltyTracker(10 * time.Minute)
	managers := service.NewManagerHierarchy(db, locks)
	events := eventstore.New(db, silentLogger())
	geoR := &geo.Resolver{}

	engine := service.NewEngine(service.EngineDeps{
		DB:        db,
		Geo:       geoR,
		Locks:     locks,
		Sessions:  sessions,
		Penalties: penalties,
		Managers:  managers,
		Events:    events,
	})

	authn := auth.NewAuthenticator(db)
	bundle := server.New(server.Deps{
		DB:     db,
		Engine: engine,
		Auth:   authn,
		Events: events,
		Logger: silentLogger(),
	})

	grpcSrv := grpc.NewServer(
		grpc.ChainUnaryInterceptor(
			server.PanicRecovery(silentLogger()),
			authn.UnaryInterceptor(),
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
	require.NoError(t, err, "grpc.NewClient")
	t.Cleanup(func() { _ = conn.Close() })

	return &testEnv{
		DB:      db,
		Engine:  engine,
		Admin:   huev1.NewAdminServiceClient(conn),
		Usage:   huev1.NewUsageServiceClient(conn),
		Locks:   locks,
		Penalty: penalties,
		Token:   plaintext,
	}
}

// authCtx attaches the manager bootstrap token as gRPC bearer auth.
func authCtx(env *testEnv) context.Context {
	return metadata.AppendToOutgoingContext(context.Background(),
		"authorization", "Bearer "+env.Token)
}

// authCtxWith uses a caller-supplied token instead of the env bootstrap.
func authCtxWith(token string) context.Context {
	return metadata.AppendToOutgoingContext(context.Background(),
		"authorization", "Bearer "+token)
}
