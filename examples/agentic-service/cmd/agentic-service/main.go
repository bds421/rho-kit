// Command agentic-service is a reference rho-kit v2.0.0 service
// demonstrating the full agentic-AI stack in one local binary:
//
//   - Multi-tenant request handling (WithMultiTenant)
//   - Per-tenant cost budgets (WithTenantBudget)
//   - Append-only signed action log (WithActionLogger)
//   - Approval workflow for destructive routes (WithApprovalStore)
//   - MCP server exposing typed handlers as tools (httpx/mcp)
//
// Run locally:
//
//	export AGENTIC_SERVICE_DEMO_TOKEN="$(openssl rand -base64 32)"
//	go run ./cmd/agentic-service
//
// Then exercise the stack:
//
//	# Tool catalog (MCP)
//	curl -s -X POST http://localhost:8080/mcp \
//	  -H 'Content-Type: application/json' \
//	  -H "Authorization: Bearer $AGENTIC_SERVICE_DEMO_TOKEN" \
//	  -H 'X-Tenant-Id: acme' \
//	  -d '{"jsonrpc":"2.0","method":"tools/list","id":1}' | jq
//
//	# Echo tool
//	curl -s -X POST http://localhost:8080/mcp \
//	  -H 'Content-Type: application/json' \
//	  -H "Authorization: Bearer $AGENTIC_SERVICE_DEMO_TOKEN" \
//	  -H 'X-Tenant-Id: acme' \
//	  -d '{"jsonrpc":"2.0","method":"echo","params":{"message":"hi"},"id":2}' | jq
//
//	# Read the demo tenant budget
//	curl -s -H "Authorization: Bearer $AGENTIC_SERVICE_DEMO_TOKEN" \
//	  -H 'X-Tenant-Id: acme' \
//	  http://localhost:8080/admin/budget | jq
//
// The example uses in-memory backends for budget, action log, and
// approval store so it stands up without external dependencies.
//
// SECURITY: this binary is an EXAMPLE. It requires a strong local demo
// bearer token, but still uses in-memory stores and an ephemeral
// action-log key. Do NOT deploy it as-is to any shared environment.
// Production wiring swaps these for redis/postgres/secret-manager
// backends and uses app.Builder so the always-on production-safety
// validator (TLS, JWT issuer/audience, internal-host loopback, sslmode)
// catches missing configuration at startup.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/bds421/rho-kit/examples/agentic-service/v2/internal/app"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	if err := app.Run(ctx); err != nil {
		logger.Error("service exited with error", "error", err)
		os.Exit(1)
	}
}
