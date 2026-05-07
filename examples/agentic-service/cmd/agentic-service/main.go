// Command agentic-service is a reference rho-kit v2.0.0 service
// demonstrating the full agentic-AI stack composed via app.Builder:
//
//   - Multi-tenant request handling (WithMultiTenant)
//   - Per-tenant cost budgets (WithTenantBudget)
//   - Append-only signed action log (WithActionLogger)
//   - Approval workflow for destructive routes (WithApprovalStore)
//   - MCP server exposing typed handlers as tools (httpx/mcp)
//
// Run locally:
//
//	go run ./cmd/agentic-service
//
// Then exercise the stack:
//
//	# Tool catalog (MCP)
//	curl -s -X POST http://localhost:8080/mcp \
//	  -H 'Content-Type: application/json' \
//	  -H 'X-Tenant-Id: acme' \
//	  -d '{"jsonrpc":"2.0","method":"tools/list","id":1}' | jq
//
//	# Echo tool
//	curl -s -X POST http://localhost:8080/mcp \
//	  -H 'Content-Type: application/json' \
//	  -H 'X-Tenant-Id: acme' \
//	  -d '{"jsonrpc":"2.0","method":"echo","params":{"message":"hi"},"id":2}' | jq
//
//	# Tenant-scoped budget headers (charge 1 per request)
//	curl -i -H 'X-Tenant-Id: acme' http://localhost:8080/healthz
//
// The example uses in-memory backends for budget, action log, and
// approval store so it stands up without external dependencies.
// Production wiring swaps these for the redis / postgres backends
// described in the per-package docs.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/bds421/rho-kit/examples/agentic-service/internal/app"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	// SECURITY: this binary is an EXAMPLE — it ships with no auth,
	// in-memory backends, and a hard-coded HMAC secret. Refuse to
	// boot in any environment that smells like production so a
	// copy-paste-into-prod misadventure crashes loud rather than
	// quietly serving an unauthenticated agent surface.
	if env := os.Getenv("KIT_ENV"); env != "" && env != "development" && env != "dev" && env != "test" && env != "local" {
		logger.Error("agentic-service example refuses to run outside dev/test environments",
			"kit_env", env,
			"hint", "this example has no auth and a public HMAC secret; use app.Builder for production wiring",
		)
		os.Exit(2)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	if err := app.Run(ctx); err != nil {
		logger.Error("service exited with error", "error", err)
		os.Exit(1)
	}
}
