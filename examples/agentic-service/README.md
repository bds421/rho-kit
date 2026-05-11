# examples/agentic-service

> **SECURITY**: this is an EXAMPLE for learning the kit. The binary
> requires a strong demo bearer token before it starts, but it still
> uses in-memory stores and omits production JWT/signed-request auth,
> distributed rate limiting, persistent approval storage, and real
> secret management. Production services use `app.Builder` end-to-end:
> `app.Builder.WithJWT / .WithSignedRequests / .WithMultiTenant /
> .WithTenantBudget / .WithActionLogger / .WithApprovalStore` and
> the per-package docs. The Builder runs an always-on validator at
> startup that rejects empty TLS, missing JWT issuer/audience,
> exposed internal-host, weak postgres sslmode, and excessive tracing
> sample rates.

A reference rho-kit v2.0.0 service that demonstrates the full
agentic-AI stack composed in one binary:

- **Multi-tenant request handling** — every request carries an
  `X-Tenant-Id`; the kit's `tenant` middleware lifts it into ctx.
- **Per-tenant cost budgets** — `data/budget/memory` enforces a
  1000-unit/minute cap per tenant.
- **Append-only signed action log** — `data/actionlog/memory` with
  HMAC signing; every MCP tool call writes an attributed entry.
- **Approval workflow** — `data/approval/memory` records destructive
  operations as `pending` → operator decides → executed.
- **MCP server** — `httpx/mcp` exposes a typed `echo` tool over
  JSON-RPC; schema is auto-generated from the input struct's
  `validate:"required"` and `desc:"..."` tags.

## Run

```bash
export AGENTIC_SERVICE_DEMO_TOKEN="$(openssl rand -base64 32)"
go run ./cmd/agentic-service
# Listens on :8080
```

## Exercise it

```bash
# Tool catalog
curl -s -X POST http://localhost:8080/mcp \
  -H 'Content-Type: application/json' \
  -H "Authorization: Bearer $AGENTIC_SERVICE_DEMO_TOKEN" \
  -H 'X-Tenant-Id: acme' \
  -d '{"jsonrpc":"2.0","method":"tools/list","id":1}' | jq

# Echo tool
curl -s -X POST http://localhost:8080/mcp \
  -H 'Content-Type: application/json' \
  -H "Authorization: Bearer $AGENTIC_SERVICE_DEMO_TOKEN" \
  -H 'X-Tenant-Id: acme' \
  -d '{"jsonrpc":"2.0","method":"echo","params":{"message":"hi"},"id":2}' | jq

# Validation failure (missing required field)
curl -s -X POST http://localhost:8080/mcp \
  -H 'Content-Type: application/json' \
  -H "Authorization: Bearer $AGENTIC_SERVICE_DEMO_TOKEN" \
  -H 'X-Tenant-Id: acme' \
  -d '{"jsonrpc":"2.0","method":"echo","params":{},"id":3}' | jq
# → {"error":{"code":-32602,"message":"..."}}

# Inspect tenant budget
curl -s -H "Authorization: Bearer $AGENTIC_SERVICE_DEMO_TOKEN" \
  -H 'X-Tenant-Id: acme' \
  http://localhost:8080/admin/budget | jq

# Trigger an approval-pending response
curl -i -X POST http://localhost:8080/admin/dangerous-action \
  -H "Authorization: Bearer $AGENTIC_SERVICE_DEMO_TOKEN" \
  -H 'X-Tenant-Id: acme' -H 'X-Actor: agent-007'
# → 202 Accepted with {"approval_id":"...","status":"pending"}
```

## What's NOT in this example

- **Production auth**: the example bearer token is a local demo
  credential. Production wraps the mux in JWT/PASETO/signedrequest
  middleware and derives actor/tenant from verified claims.
- **Persistence**: in-memory backends are sufficient to demo the API
  shape but evaporate on restart. Production swaps in
  `data/budget/redis`, `data/actionlog/postgres`,
  `data/approval/postgres`.
- **app.Builder wiring**: the example uses a hand-composed mux for
  clarity. Real services use `app.Builder.WithMultiTenant /
  .WithTenantBudget / .WithActionLogger / .WithApprovalStore` and
  let the Builder install the middleware chain in the right order.
- **Persistent HMAC key management**: the action-log secret is
  generated per process because the demo has no persistent log. A
  restart creates a new chain. Production must load stable signing
  keys from KMS, env vars, or a secret manager.

## Where the smoke test lives

`internal/app/app_test.go` exercises the MCP echo tool round trip
and the `-32602 Invalid params` validation-failure path. CI runs
this on every PR through the root Makefile gates; locally, use
`go test ./examples/agentic-service/...`.
