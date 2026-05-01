// Command hue runs the Hiddify Usage Engine: a single binary that serves
// gRPC and HTTP-via-gRPC-gateway on one TLS port.
//
// Lifecycle:
//   1. Load HUE_* environment.
//   2. Open Postgres via ent (optional auto-migrate).
//   3. Bootstrap a manager API key if HUE_BOOTSTRAP_TOKEN is set.
//   4. Build engine + server bundle.
//   5. Start an in-process gRPC over a bufconn listener (so gRPC-gateway
//      can dial the same handlers and every request — gRPC or REST —
//      goes through the same interceptors).
//   6. Start a single http.Server on HUE_ADDR with a content-type-sniffing
//      handler that routes HTTP/2 application/grpc traffic to the gRPC
//      server and everything else to the gateway mux.
//   7. Mark gRPC health = SERVING.
//   8. On SIGINT/SIGTERM: NOT_SERVING → graceful gRPC stop → http Shutdown
//      → close ent → exit.
package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"buf.build/go/protovalidate"
	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/reflection"
	"google.golang.org/grpc/test/bufconn"

	huev1 "github.com/hiddify/hue/gen/go/hue/v1"
	"github.com/hiddify/hue/internal/auth"
	"github.com/hiddify/hue/internal/config"
	"github.com/hiddify/hue/internal/database"
	"github.com/hiddify/hue/internal/eventstore"
	"github.com/hiddify/hue/internal/geo"
	"github.com/hiddify/hue/internal/server"
	"github.com/hiddify/hue/internal/service"
)

// Build-time variables set by the linker (-ldflags). See Makefile.
var (
	version = "dev"
	commit  = "unknown"
	date    = "unknown"
)

func main() {
	// Tiny "healthcheck" subcommand for use in the Dockerfile HEALTHCHECK,
	// avoiding a curl dependency. Connects to /healthz on localhost and
	// exits 0 on 200, 1 otherwise.
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		os.Exit(runHealthcheckSubcmd())
	}

	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "hue: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	cfg, err := config.Load(ctx)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	logger := newLogger(cfg)
	slog.SetDefault(logger)
	logger.Info("starting hue",
		"version", version,
		"commit", commit,
		"date", date,
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

	// --- Bootstrap ---
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

	// --- gRPC server with interceptors (auth + validate + recovery + log) ---
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
	// Register the gRPC server reflection service so grpcurl + grpc-ui
	// work without needing the .proto file. Safe to leave on in
	// production: it only exposes the schema, not data.
	reflection.Register(grpcSrv)

	// --- In-process bufconn so the gateway hits the same gRPC stack ---
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

	// --- Single-port handler ---
	rootHandler := http.NewServeMux()
	rootHandler.HandleFunc("/openapi.json", openapiHandler)
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

	// --- Listen + serve ---
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

	// --- Wait for signal or serve error ---
	select {
	case <-ctx.Done():
		logger.Info("signal received, shutting down")
	case err := <-serveErr:
		if !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("serve: %w", err)
		}
	}

	// --- Graceful shutdown: stop accepting → drain → close ---
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

// openapiHandler serves the buf-generated OpenAPI document.
func openapiHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-cache")
	// The file is bundled into the binary in CI via go:embed in a future
	// iteration; for now we fall through to a 404 message so deploys
	// without bundling don't crash.
	http.Error(w, `{"error":"openapi.json not bundled"}`, http.StatusNotFound)
}

// newLogger returns a zap-replaced log/slog logger. Format and level are
// controlled by HUE_LOG_FORMAT and HUE_LOG_LEVEL.
func newLogger(cfg *config.Config) *slog.Logger {
	level := slog.LevelInfo
	switch strings.ToLower(cfg.LogLevel) {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}
	opts := &slog.HandlerOptions{Level: level}
	var h slog.Handler
	if strings.EqualFold(cfg.LogFormat, "text") {
		h = slog.NewTextHandler(os.Stderr, opts)
	} else {
		h = slog.NewJSONHandler(os.Stderr, opts)
	}
	return slog.New(h)
}

// runHealthcheckSubcmd performs a self-Health/Check over loopback. Used
// by the Docker HEALTHCHECK directive; never called during normal runs.
func runHealthcheckSubcmd() int {
	addr := os.Getenv("HUE_ADDR")
	if addr == "" {
		addr = ":8443"
	}
	target := "http://localhost" + addr + "/healthz"
	if strings.HasPrefix(addr, ":") {
		// :8443 form
	}
	client := &http.Client{Timeout: 3 * time.Second}
	req, _ := http.NewRequest(http.MethodGet, target, nil)
	resp, err := client.Do(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "healthcheck: %v\n", err)
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "healthcheck: status %d\n", resp.StatusCode)
		return 1
	}
	return 0
}
