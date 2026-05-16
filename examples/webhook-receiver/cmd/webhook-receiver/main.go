// Command webhook-receiver is a rho-kit v2.0.0 reference service
// demonstrating the canonical webhook-ingest pattern:
//
//   - HMAC signature verification via signedrequest middleware
//   - Replay protection via in-memory nonce store
//   - Idempotency cache via the kit's idempotency middleware so
//     retries from the upstream return the cached response instead
//     of double-processing
//   - Single typed handler with JSON validation
//
// Run locally:
//
//	export WEBHOOK_HMAC_KEY="$(openssl rand -hex 32)"
//	go run ./cmd/webhook-receiver
//	# Listens on :8090
//
// Exercise it with the bundled sign-and-send helper documented in
// README.md.
//
// SECURITY: this is an EXAMPLE. It uses in-memory stores for both
// nonces and idempotency entries, an in-memory body cap, and a
// single static HMAC key resolver. Production deployments swap:
//
//   - the nonce store for a Redis-backed implementation that
//     survives restart and shares state across replicas;
//   - the idempotency store for `data/idempotency/redisstore` or
//     `data/idempotency/pgstore` (the example's `NewMemoryStore`
//     would be flagged by `kit-doctor`);
//   - the static KeyResolver for one that fetches per-tenant keys
//     from a secret manager.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/bds421/rho-kit/examples/webhook-receiver/v2/internal/app"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	if err := app.Run(ctx); err != nil {
		logger.Error("webhook-receiver exited with error", "error", err)
		os.Exit(1)
	}
}
