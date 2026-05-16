// Command realtime-broadcast is a rho-kit v2.0.0 reference service
// demonstrating the canonical browser-facing real-time composition:
//
//   - realtime/centrifuge.NewNode (WebSocket transport, channel
//     subscribe/publish, presence, history — all the heavy
//     lifting handled by the centrifuge library)
//   - security/jwtutil.Provider for connect-time JWT verification
//     (issuer, audience, signature, expiry)
//   - centrifuge.WithChannelClassifier projecting channel names
//     through bounded-cardinality "class" labels so per-tenant
//     channel names cannot inflate Prometheus cardinality
//
// Run locally:
//
//	go run ./cmd/realtime-broadcast
//	# Listens on :8096; centrifuge websocket at /connection/websocket
//	# Token endpoint at /demo/token
//
// SECURITY: this is an EXAMPLE. It generates an ECDSA keypair at
// startup and signs demo tokens with the corresponding private
// key — the JWKS public-half is exposed at /demo/jwks for the
// kit's own verifier to consume. Production deployments load
// keys from a real issuer (Auth0, Cognito, Keycloak, custom IDP)
// and never expose the signing private key to the broadcast
// server. The example collapses both sides into one binary for
// pedagogy.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/bds421/rho-kit/examples/realtime-broadcast/v2/internal/app"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	if err := app.Run(ctx); err != nil {
		logger.Error("realtime-broadcast exited with error", "error", err)
		os.Exit(1)
	}
}
