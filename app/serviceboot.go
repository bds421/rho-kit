package app

import (
	"log/slog"
	"os"

	"github.com/bds421/rho-kit/core/v2/config"
	"github.com/bds421/rho-kit/core/v2/redact"
	"github.com/bds421/rho-kit/observability/v2/health"
	"github.com/bds421/rho-kit/observability/v2/logging"
)

// Main is the standard entry point that replaces the identical main() function
// in every service. It handles --health flag, structured logger setup, and os.Exit.
// The logger is enriched with service name and version attributes, and bridges
// with OpenTelemetry to inject trace/span IDs into log output.
//
// Log level defaults to "info" and can be overridden via LOG_LEVEL env var.
//
// Fatal-startup error logging: emits the redacted message (kit
// convention — error strings can carry SDK URLs, broker credentials,
// DSN fragments, or validator output) PLUS the unwrap chain of concrete
// Go types (kit-controlled / SDK-controlled identifiers, no
// caller-supplied content). Operators get enough triage information to
// identify the failing subsystem ("config.LoadError → os.PathError")
// without leaking the underlying message.
func Main(name, version string, runFn func(logger *slog.Logger) error) {
	if len(os.Args) > 1 && os.Args[1] == "--health" {
		health.RunHealthCheck(resolveHealthCheckPort())
	}

	logger := logging.New(logging.Config{
		Level:          config.Get("LOG_LEVEL", "info"),
		ServiceName:    name,
		ServiceVersion: health.ResolveVersion(version),
	})
	slog.SetDefault(logger)

	logger.Info("starting service")

	if err := runFn(logger); err != nil {
		logger.Error("application error",
			redact.Error(err),
			redact.ErrorChain(err),
		)
		os.Exit(1)
	}
}

// defaultInternalPort is the fallback port for the internal ops server
// (health, ready, metrics). It mirrors the INTERNAL_PORT default used by
// [LoadBaseConfig], so the --health probe and the running server agree on
// the port when INTERNAL_PORT is left unset.
const defaultInternalPort = 9090

// resolveHealthCheckPort reads the internal ops port from INTERNAL_PORT,
// falling back to [defaultInternalPort]. The --health flag short-circuits
// before LoadBaseConfig runs, so it must read the same env var directly;
// otherwise a service that overrides INTERNAL_PORT (and wires the documented
// Docker HEALTHCHECK --health flag) would probe the wrong port and be
// reported permanently unhealthy. A malformed INTERNAL_PORT falls back to the
// default rather than failing the probe — the probe target then matches the
// default the server would also fall back to, and the real config error is
// surfaced by the normal startup path.
func resolveHealthCheckPort() int {
	port, err := config.GetInt("INTERNAL_PORT", defaultInternalPort)
	if err != nil {
		return defaultInternalPort
	}
	return port
}
