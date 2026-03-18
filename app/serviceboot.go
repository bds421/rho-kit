package app

import (
	"log/slog"
	"os"

	"github.com/bds421/rho-kit/core/config"
	"github.com/bds421/rho-kit/observability/health"
	"github.com/bds421/rho-kit/observability/logging"
)

// Main is the standard entry point that replaces the identical main() function
// in every service. It handles --health flag, structured logger setup, and os.Exit.
// The logger is enriched with service name and version attributes, and bridges
// with OpenTelemetry to inject trace/span IDs into log output.
//
// Log level defaults to "info" and can be overridden via LOG_LEVEL env var.
func Main(name, version string, runFn func(logger *slog.Logger) error) {
	if len(os.Args) > 1 && os.Args[1] == "--health" {
		health.RunHealthCheck(9090)
	}

	logger := logging.New(logging.Config{
		Level:          config.Get("LOG_LEVEL", "info"),
		ServiceName:    name,
		ServiceVersion: health.ResolveVersion(version),
	})
	slog.SetDefault(logger)

	logger.Info("starting service")

	if err := runFn(logger); err != nil {
		logger.Error("application error", "error", err)
		os.Exit(1)
	}
}
