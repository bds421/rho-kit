// Command api-gateway is a rho-kit v2.0.0 reference service
// demonstrating the canonical public-facing service composition:
//
//   - IP-keyed rate limit (httpx/middleware/ratelimit)
//   - JWT validation (stubbed in the example; production wires
//     security/jwtutil or the app/jwt bridge)
//   - Downstream fan-out wrapped with resilience/circuitbreaker
//     and resilience/retry
//
// Run locally:
//
//	go run ./cmd/api-gateway
//	# Listens on :8095
//
// SECURITY: this is an EXAMPLE. It uses an in-memory rate limiter,
// a stubbed bearer-token validator, and a synthetic downstream
// function. Production wiring lives in app.Builder — see
// examples/README.md "Recipe: api-gateway" for the canonical
// Builder composition that runs the kit's always-on production-
// safety validator (TLS, JWT issuer/audience, internal-host
// loopback, sslmode, tracing sample rate).
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/bds421/rho-kit/examples/api-gateway/v2/internal/app"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	if err := app.Run(ctx); err != nil {
		logger.Error("api-gateway exited with error", "error", err)
		os.Exit(1)
	}
}
