// Command hue is the Hiddify Usage Engine binary.
//
// All real work lives in the root "github.com/hiddify/hue" package; this
// file is a thin Cobra-based CLI that picks a subcommand and delegates.
// External programs that want HUE as a library can import the root
// package directly and call hue.Run themselves.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/hiddify/hue"
)

func main() {
	if err := newRootCmd().Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "hue: %v\n", err)
		os.Exit(1)
	}
}

func newRootCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "hue",
		Short: "Hiddify Usage Engine",
		Long: `HUE — protocol-agnostic usage tracking and subscription
control plane. Serves gRPC and grpc-gateway-translated REST on a
single port. Configured entirely via HUE_* environment variables.`,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE:          runServe, // default action when no subcommand
	}

	// -v / --version on the root command. Implemented by hand so it works
	// uniformly with subcommands and so we control the output format.
	cmd.PersistentFlags().BoolP("version", "v", false, "Print version and exit")
	cmd.PersistentPreRunE = func(c *cobra.Command, _ []string) error {
		if v, _ := c.Flags().GetBool("version"); v {
			fmt.Println(hue.BuildInfo())
			os.Exit(0)
		}
		return nil
	}

	cmd.AddCommand(
		newServeCmd(),
		newVersionCmd(),
		newHealthCmd(),
	)
	return cmd
}

// ----------------------------------------------------------------------
// hue serve  (also the default action when invoked as just `hue`)
// ----------------------------------------------------------------------

func newServeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "serve",
		Short: "Run the HUE server (default)",
		RunE:  runServe,
	}
}

func runServe(cmd *cobra.Command, _ []string) error {
	ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	cfg, err := hue.LoadConfig(ctx)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	logger := newLogger(cfg)
	slog.SetDefault(logger)

	return hue.Run(ctx, cfg, logger)
}

// ----------------------------------------------------------------------
// hue version
// ----------------------------------------------------------------------

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print build info",
		Run: func(_ *cobra.Command, _ []string) {
			fmt.Println(hue.BuildInfo())
		},
	}
}

// ----------------------------------------------------------------------
// hue healthcheck — used by the Docker HEALTHCHECK so we don't need curl
// ----------------------------------------------------------------------

func newHealthCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "healthcheck",
		Short: "Self health-check (used by Docker HEALTHCHECK)",
		RunE: func(_ *cobra.Command, _ []string) error {
			addr := os.Getenv("HUE_ADDR")
			if addr == "" {
				addr = ":8443"
			}
			target := "http://localhost" + addr + "/healthz"
			client := &http.Client{Timeout: 3 * time.Second}
			req, _ := http.NewRequest(http.MethodGet, target, nil)
			resp, err := client.Do(req)
			if err != nil {
				return fmt.Errorf("healthcheck: %w", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				return fmt.Errorf("healthcheck: status %d", resp.StatusCode)
			}
			return nil
		},
	}
}

// ----------------------------------------------------------------------
// Logging — local helper to honor HUE_LOG_LEVEL / HUE_LOG_FORMAT.
// Library callers control logging themselves and don't go through here.
// ----------------------------------------------------------------------

func newLogger(cfg *hue.Config) *slog.Logger {
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

// (root package vars version/commit/date are wired via -ldflags through
// the github.com/hiddify/hue.Version etc. symbols; we don't shadow them
// here — the build flags target the public symbols directly.)

// silence "imported and not used" warning when context isn't referenced
// in any other helper above (cmd.Context returns context.Context and is
// used in runServe). This anchor keeps the import set unconditionally.
var _ = context.Background