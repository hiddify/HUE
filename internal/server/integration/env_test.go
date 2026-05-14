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

// testEnv: ent client + typed gRPC clients + Owner bootstrap token.
type testEnv struct {
	DB *ent.Client

	Admin             huev1.AdminServiceClient
	Auth              huev1.AuthServiceClient
	AuthAdmin         huev1.AuthAdminServiceClient
	Certs             huev1.DomainCertificateServiceClient
	Config            huev1.ConfigServiceClient
	ResellerClients   huev1.ResellerClientServiceClient
	Resellers         huev1.ResellerManagementServiceClient
	Usage             huev1.UsageServiceClient

	OwnerToken string // plaintext Owner API key minted via Bootstrap.
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	ctx := context.Background()

	schema := uniqueSchemaName()
	dropSchema := createSchema(t, schema)
	t.Cleanup(dropSchema)

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
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	_, owner, _, err := auth.GenerateKey(auth.KindOwner)
	require.NoError(t, err)
	require.NoError(t, auth.Bootstrap(ctx, db, owner, silentLogger()))

	signingKey, err := auth.EnsureSigningKey(ctx, db)
	require.NoError(t, err)

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
		DB:         db,
		Engine:     engine,
		Auth:       authn,
		Resellers:  resellers,
		SigningKey: signingKey,
		Lockout:    auth.NewLockout(0, 0),
		Events:     events,
		Logger:     silentLogger(),
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
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	return &testEnv{
		DB:              db,
		Admin:           huev1.NewAdminServiceClient(conn),
		Auth:            huev1.NewAuthServiceClient(conn),
		AuthAdmin:       huev1.NewAuthAdminServiceClient(conn),
		Certs:           huev1.NewDomainCertificateServiceClient(conn),
		Config:          huev1.NewConfigServiceClient(conn),
		ResellerClients: huev1.NewResellerClientServiceClient(conn),
		Resellers:       huev1.NewResellerManagementServiceClient(conn),
		Usage:           huev1.NewUsageServiceClient(conn),
		OwnerToken:      owner,
	}
}

// ownerCtx attaches the Owner API key as gRPC bearer auth.
func ownerCtx(env *testEnv) context.Context {
	return metadata.AppendToOutgoingContext(context.Background(),
		"authorization", "Bearer "+env.OwnerToken)
}

// jwtCtx attaches a JWT bearer (from a Login response) as auth.
func jwtCtx(jwt string) context.Context {
	return metadata.AppendToOutgoingContext(context.Background(),
		"authorization", "Bearer "+jwt)
}
