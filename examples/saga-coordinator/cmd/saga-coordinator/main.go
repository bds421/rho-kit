// Command saga-coordinator is a rho-kit v2.0.0 reference service
// demonstrating the canonical multi-step transaction pattern with
// compensation, idempotent retry safety, and exclusive execution.
//
// Composes:
//   - runtime/saga (Step / Definition / Run, with Compensate
//     callbacks invoked in reverse on roll-forward failure)
//   - data/idempotency (in-memory store wrapping the saga so a
//     retry with the same key returns the cached result instead
//     of re-executing the steps)
//   - in-process mutex around the saga key so concurrent retries
//     from the same caller serialize cleanly. Production wires
//     data/lock/pgadvisory.Acquire instead.
//
// Run locally:
//
//	go run ./cmd/saga-coordinator
//	# Listens on :8097; POST /orders runs the saga with an
//	# Idempotency-Key header for retry safety.
//
// SECURITY: this is an EXAMPLE. The idempotency store is in-memory
// (single-process; the kit-doctor `idempotency-memory-store`
// rule is suppressed inline). Production wires
// `data/idempotency/pgstore` or `data/idempotency/redisstore`
// for cross-replica replay protection, and combines the
// in-memory mutex with `data/lock/pgadvisory.Acquire` so the
// saga's exclusive section survives replica failover.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/bds421/rho-kit/examples/saga-coordinator/v2/internal/app"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	if err := app.Run(ctx); err != nil {
		logger.Error("saga-coordinator exited with error", "error", err)
		os.Exit(1)
	}
}
