package main

import (
	"context"
	"fmt"
	"net"
	stdhttp "net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/hiddify/hue-go/internal/api/grpc"
	httpapi "github.com/hiddify/hue-go/internal/api/http"
	"github.com/hiddify/hue-go/internal/config"
	"github.com/hiddify/hue-go/internal/engine"
	"github.com/hiddify/hue-go/internal/eventstore"
	"github.com/hiddify/hue-go/internal/storage/cache"
	"github.com/hiddify/hue-go/internal/storage/sqlite"
	"go.uber.org/zap"
)

func main() {
	// Initialize logger
	logger, err := zap.NewProduction()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to initialize logger: %v\n", err)
		os.Exit(1)
	}
	defer logger.Sync()

	// Load configuration
	cfg, err := config.Load()
	if err != nil {
		logger.Fatal("Failed to load config", zap.Error(err))
	}

	// Debug: print loaded secret
	logger.Info("Config loaded", zap.String("auth_secret", cfg.AuthSecret))

	// Set log level
	if cfg.LogLevel == "debug" {
		logger = logger.With(zap.String("level", "debug"))
	}

	logger.Info("Starting HUE - Hiddify Usage Engine",
		zap.String("version", "1.0.0"),
		zap.String("port", cfg.Port),
	)

	// Initialize database layer
	userDB, err := sqlite.NewUserDB(cfg.DatabaseURL)
	if err != nil {
		logger.Fatal("Failed to initialize user database", zap.Error(err))
	}
	defer userDB.Close()

	activeDB, err := sqlite.NewActiveDB(cfg.DatabaseURL)
	if err != nil {
		logger.Fatal("Failed to initialize active database", zap.Error(err))
	}
	defer activeDB.Close()

	historyDB, err := sqlite.NewHistoryDB(cfg.DatabaseURL)
	if err != nil {
		logger.Fatal("Failed to initialize history database", zap.Error(err))
	}
	defer historyDB.Close()

	// Run migrations
	if err := userDB.Migrate(); err != nil {
		logger.Fatal("Failed to run migrations", zap.Error(err))
	}

	// Initialize in-memory cache
	memCache := cache.NewMemoryCache()

	// Initialize event store
	eventStore, err := eventstore.New(cfg.EventStoreType, historyDB)
	if err != nil {
		logger.Fatal("Failed to initialize event store", zap.Error(err))
	}

	// Initialize core engine
	quotaEngine := engine.NewQuotaEngine(userDB, activeDB, memCache, logger)
	sessionManager := engine.NewSessionManager(memCache, cfg.ConcurrentWindow, logger)
	penaltyHandler := engine.NewPenaltyHandler(memCache, cfg.PenaltyDuration, logger)
	geoHandler, err := engine.NewGeoHandler(cfg.MaxMindDBPath)
	if err != nil {
		logger.Warn("GeoIP handler not initialized, geo features disabled", zap.Error(err))
	}

	// Create context with cancellation
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start buffered write system
	flushTicker := time.NewTicker(cfg.DBFlushInterval)
	defer flushTicker.Stop()

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-flushTicker.C:
				if err := activeDB.Flush(); err != nil {
					logger.Error("Failed to flush active database", zap.Error(err))
				}
			}
		}
	}()

	// Initialize gRPC server
	grpcServer := grpc.NewServer(
		quotaEngine,
		sessionManager,
		penaltyHandler,
		geoHandler,
		eventStore,
		logger,
		cfg.AuthSecret,
	)
	grpcServer.SetUserDB(userDB)

	// Start gRPC listener
	lis, err := net.Listen("tcp", ":"+cfg.Port)
	if err != nil {
		logger.Fatal("Failed to listen on gRPC port", zap.Error(err))
	}

	go func() {
		logger.Info("gRPC server starting", zap.String("port", cfg.Port))
		if err := grpcServer.Serve(lis); err != nil {
			logger.Error("gRPC server error", zap.Error(err))
		}
	}()

	// Initialize HTTP server
	httpRouter := httpapi.NewServer(
		userDB,
		activeDB,
		quotaEngine,
		logger,
		cfg.AuthSecret,
	)

	httpLis, err := net.Listen("tcp", ":"+cfg.HTTPPort)
	if err != nil {
		logger.Fatal("Failed to listen on HTTP port", zap.Error(err))
	}

	httpServer := &stdhttp.Server{
		Handler: httpRouter,
	}

	go func() {
		logger.Info("HTTP server starting", zap.String("port", cfg.HTTPPort))
		if err := httpServer.Serve(httpLis); err != nil && err != stdhttp.ErrServerClosed {
			logger.Error("HTTP server error", zap.Error(err))
		}
	}()

	// Graceful shutdown
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	logger.Info("Shutting down HUE...")

	// Final flush before shutdown
	if err := activeDB.Flush(); err != nil {
		logger.Error("Failed to flush on shutdown", zap.Error(err))
	}

	// Stop servers
	grpcServer.GracefulStop()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()

	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		logger.Error("HTTP server shutdown error", zap.Error(err))
	}

	// Close geo handler
	if geoHandler != nil {
		geoHandler.Close()
	}

	logger.Info("HUE shutdown complete")
}
