// Package hue exposes the runtime entry points of the Hiddify Usage
// Engine so it can be embedded as a library in addition to running as
// the cmd/hue binary.
//
// Two surfaces:
//
//   - hue.Config + hue.LoadConfig — the configuration type, lifted out
//     of internal/ so external programs can construct it programmatically
//     instead of being limited to environment variables.
//
//   - hue.Run — the all-in-one server lifecycle. Opens Postgres via ent,
//     bootstraps an API key when configured, wires the engine, registers
//     gRPC + grpc-gateway on a single port behind h2c (or TLS), and
//     blocks until ctx is canceled. cmd/hue calls it; library users can
//     too.
//
// Build-time variables Version, Commit, and Date are also public so
// embeddings can echo the same information from their own --version output.
package hue

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"buf.build/go/protovalidate"
	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"github.com/sethvargo/go-envconfig"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/reflection"
	"google.golang.org/grpc/test/bufconn"

	huev1 "github.com/hiddify/hue/gen/go/hue/v1"
	"github.com/hiddify/hue/internal/auth"
	"github.com/hiddify/hue/internal/database"
	"github.com/hiddify/hue/internal/eventstore"
	"github.com/hiddify/hue/internal/geo"
	"github.com/hiddify/hue/internal/server"
	"github.com/hiddify/hue/internal/service"
)

// Build info — overridable via -ldflags. Library consumers can read these
// to surface the same info from their own --version output.
var (
	Version = "dev"
	Commit  = "unknown"
	Date    = "unknown"
)

// BuildInfo returns a one-line description of the linked binary:
// "<version> (commit <c>, built <d>)".
func BuildInfo() string {
	return fmt.Sprintf("%s (commit %s, built %s)", Version, Commit, Date)
}

// Config is the runtime configuration. Construct it in code or load it
// from the environment via LoadConfig.
//
// For Docker / Kubernetes secret-file mounts, set HUE_<KEY>_FILE=/path
// and LoadConfig reads its contents into HUE_<KEY> before parsing.
type Config struct {
	// Network
	Addr        string `env:"HUE_ADDR, default=:8443"`
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
	LogFormat string `env:"HUE_LOG_FORMAT, default=json"`

	// Shutdown
	ShutdownTimeout time.Duration `env:"HUE_SHUTDOWN_TIMEOUT, default=30s"`
}

// LoadConfig reads HUE_* env vars into a Config.
func LoadConfig(ctx context.Context) (*Config, error) {
	if err := expandFileEnvs(); err != nil {
		return nil, err
	}
	var cfg Config
	if err := envconfig.Process(ctx, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

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

// Run starts HUE and blocks until ctx is done or a fatal error occurs.
//
// Lifecycle:
//   1. Open Postgres (auto-migrates if cfg.AutoMigrate).
//   2. Bootstrap a manager API key if cfg.BootstrapToken is non-empty
//      (idempotent — skipped when one already exists).
//   3. Build the engine + server bundle.
//   4. Start an in-process gRPC server reachable via bufconn so
//      gRPC-gateway hits the same auth + validation interceptors.
//   5. Start a single http.Server on cfg.Addr — TLS when both
//      TLSCertFile and TLSKeyFile are set, else h2c.
//   6. Mark gRPC health = SERVING.
//   7. On ctx.Done(): NOT_SERVING → http.Shutdown(timeout) →
//      grpcServer.GracefulStop → Close DB → return.
//
// Returns nil on graceful shutdown, non-nil only on fatal startup or
// serve errors (canceled context is not an error).
func Run(ctx context.Context, cfg *Config, logger *slog.Logger) error {
	if logger == nil {
		logger = slog.Default()
	}

	logger.Info("starting hue",
		"version", Version,
		"commit", Commit,
		"date", Date,
		"addr", cfg.Addr,
	)

	// --- Database ---
	dbClient, err := database.Open(ctx, database.Config{
		DSN:          cfg.DatabaseURL,
		MaxOpenConns: cfg.MaxOpenConns,
		MaxIdleConns: cfg.MaxIdleConns,
		AutoMigrate:  cfg.AutoMigrate,
	})
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	defer func() { _ = dbClient.Close() }()

	if err := auth.Bootstrap(ctx, dbClient, cfg.BootstrapToken, logger); err != nil {
		return fmt.Errorf("bootstrap: %w", err)
	}

	// --- Engine deps ---
	geoResolver, err := geo.New(cfg.MaxMindDBPath)
	if err != nil {
		logger.Warn("geo disabled", "err", err)
		geoResolver = &geo.Resolver{}
	}
	defer func() { _ = geoResolver.Close() }()

	locks := service.NewLockManager()
	sessions := service.NewSessionTracker(cfg.ConcurrentWindow)
	penalties := service.NewPenaltyTracker(cfg.PenaltyDuration)
	managers := service.NewManagerHierarchy(dbClient, locks)
	events := eventstore.New(dbClient, logger)

	engine := service.NewEngine(service.EngineDeps{
		DB:        dbClient,
		Geo:       geoResolver,
		Locks:     locks,
		Sessions:  sessions,
		Penalties: penalties,
		Managers:  managers,
		Events:    events,
	})

	// --- Validator + auth ---
	validator, err := protovalidate.New()
	if err != nil {
		return fmt.Errorf("init protovalidate: %w", err)
	}
	authn := auth.NewAuthenticator(dbClient)

	// --- Server bundle ---
	bundle := server.New(server.Deps{
		DB:     dbClient,
		Engine: engine,
		Auth:   authn,
		Events: events,
		Logger: logger,
	})

	grpcSrv := grpc.NewServer(
		grpc.ChainUnaryInterceptor(
			server.PanicRecovery(logger),
			server.SlogUnary(logger),
			authn.UnaryInterceptor(),
			server.Validate(validator),
		),
		grpc.ChainStreamInterceptor(
			server.PanicRecoveryStream(logger),
			authn.StreamInterceptor(),
		),
	)
	bundle.Register(grpcSrv)
	reflection.Register(grpcSrv)

	const bufSize = 1 << 20
	bufLis := bufconn.Listen(bufSize)
	go func() {
		if err := grpcSrv.Serve(bufLis); err != nil {
			logger.Error("grpc bufconn serve", "err", err)
		}
	}()

	gwConn, err := grpc.NewClient(
		"passthrough://buf",
		grpc.WithContextDialer(func(_ context.Context, _ string) (net.Conn, error) {
			return bufLis.Dial()
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return fmt.Errorf("gateway dial: %w", err)
	}
	defer func() { _ = gwConn.Close() }()

	gwMux := runtime.NewServeMux(
		runtime.WithIncomingHeaderMatcher(headerMatcher),
		runtime.WithMarshalerOption(runtime.MIMEWildcard, &runtime.JSONPb{}),
	)
	if err := huev1.RegisterAdminServiceHandler(ctx, gwMux, gwConn); err != nil {
		return fmt.Errorf("register admin gateway: %w", err)
	}
	if err := huev1.RegisterUsageServiceHandler(ctx, gwMux, gwConn); err != nil {
		return fmt.Errorf("register usage gateway: %w", err)
	}
	if err := huev1.RegisterNodeServiceHandler(ctx, gwMux, gwConn); err != nil {
		return fmt.Errorf("register node gateway: %w", err)
	}

	rootHandler := http.NewServeMux()
	rootHandler.Handle("/", gwMux)

	dispatch := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isGRPC(r) {
			grpcSrv.ServeHTTP(w, r)
			return
		}
		rootHandler.ServeHTTP(w, r)
	})

	httpSrv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           h2c.NewHandler(dispatch, &http2.Server{}),
		ReadHeaderTimeout: 10 * time.Second,
	}

	if cfg.TLSCertFile != "" && cfg.TLSKeyFile != "" {
		cert, err := tls.LoadX509KeyPair(cfg.TLSCertFile, cfg.TLSKeyFile)
		if err != nil {
			return fmt.Errorf("load tls: %w", err)
		}
		httpSrv.TLSConfig = &tls.Config{
			Certificates: []tls.Certificate{cert},
			MinVersion:   tls.VersionTLS12,
			NextProtos:   []string{"h2", "http/1.1"},
		}
	}

	ln, err := net.Listen("tcp", cfg.Addr)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	logger.Info("listening", "addr", ln.Addr().String(), "tls", httpSrv.TLSConfig != nil)
	bundle.MarkServing()

	serveErr := make(chan error, 1)
	go func() {
		if httpSrv.TLSConfig != nil {
			tlsLn := tls.NewListener(ln, httpSrv.TLSConfig)
			serveErr <- httpSrv.Serve(tlsLn)
		} else {
			serveErr <- httpSrv.Serve(ln)
		}
	}()

	select {
	case <-ctx.Done():
		logger.Info("signal received, shutting down")
	case err := <-serveErr:
		if !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("serve: %w", err)
		}
	}

	bundle.Shutdown()
	shutCtx, shutCancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer shutCancel()
	if err := httpSrv.Shutdown(shutCtx); err != nil {
		logger.Warn("http shutdown", "err", err)
	}
	grpcSrv.GracefulStop()
	return nil
}

// isGRPC reports whether r looks like an inbound gRPC request — HTTP/2
// + Content-Type starting with application/grpc.
func isGRPC(r *http.Request) bool {
	return r.ProtoMajor == 2 && strings.HasPrefix(r.Header.Get("Content-Type"), "application/grpc")
}

// headerMatcher passes the Authorization header through to gRPC metadata
// so the auth interceptor sees REST requests with the same shape as
// native gRPC ones.
func headerMatcher(key string) (string, bool) {
	switch strings.ToLower(key) {
	case "authorization":
		return "authorization", true
	}
	return runtime.DefaultHeaderMatcher(key)
}
