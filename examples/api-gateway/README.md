# examples/api-gateway

> **SECURITY**: this is an EXAMPLE for learning the rho-kit
> public-facing service composition. The binary uses a STUBBED
> bearer-token validator, an in-memory rate limiter, and a
> synthetic downstream function. Production deployments wire
> `app.Builder` with the real `jwt.Module`, `ratelimit.IP`,
> downstream clients, and let the Builder's startup validator
> enforce TLS / JWT issuer-audience / sslmode constraints.
> See "Production wiring" below.

A reference rho-kit v2.0.0 service that demonstrates the canonical
public-facing service composition:

```
ratelimit.Middleware            (IP-keyed throttle)
  → jwtAuthMiddleware           (bearer-token validation; STUB)
    → downstream-fanout handler
         → timeoutbudget.New     (request-scoped time budget,
                                  reserves 50ms for response-write)
           → bulkhead.ExecuteCtx (cap concurrent in-flight to 8)
             → budget.WithRemaining (derive per-call deadline)
               → circuitbreaker.ExecuteCtx (fast-fail on broken downstream)
                 → retry.DoWith             (transient blip recovery)
                   → real downstream call
```

The order is load-bearing:

1. **Rate limit OUTERMOST.** DDoS shedding happens before any
   auth or downstream work. A flood of unauthenticated requests
   never reaches the JWT validator.
2. **JWT auth SECOND.** Unauthenticated requests do not consume
   downstream budget. The example uses a constant-time bearer
   comparison via `crypto/subtle`; production wires
   `security/jwtutil` (or the `app/jwt` bridge module).
3. **timeoutbudget WRAPS the chain.** A request-total deadline is
   established once, then every retry inherits a shrinking child
   deadline. A 50ms reservation guarantees the response-write +
   observability emit cannot be starved by a slow downstream.
4. **Bulkhead OUTSIDE breaker.** The cap on concurrent in-flight
   applies regardless of breaker state, so a flood cannot consume
   breaker slots or saturate the downstream. Callers arriving
   while full wait up to 100ms before being rejected with 503.
5. **Breaker(retry(call)) for downstream.** Breaker is OUTER so
   when downstream is broken, the breaker rejects fast (returning
   503) WITHOUT burning retries. Retry is INNER so transient
   blips inside a half-open breaker still get a couple of
   attempts. The kit's wave 169 OTel tracing records
   `ErrCircuitOpen` as an attribute (not a span error) — open
   circuits are steady-state, not exceptions.

Each failure mode maps to a distinct status code so operators can
route incidents correctly:

| Layer | Outcome | Status |
|-------|---------|--------|
| circuitbreaker | open  | 503 `downstream unavailable (circuit open)` |
| bulkhead       | full  | 503 `downstream busy (bulkhead full)` |
| timeoutbudget  | expired | 504 `downstream timed out (budget exhausted)` |
| downstream     | error | 502 `downstream call failed` |

## Run

```bash
export API_GATEWAY_DEMO_TOKEN="$(openssl rand -hex 16)"
go run ./cmd/api-gateway
# Listens on :8095
```

## Exercise it

```bash
# Happy path
curl -s http://localhost:8095/api/orders \
  -H "Authorization: Bearer $API_GATEWAY_DEMO_TOKEN" \
  -H "X-Tenant-Id: acme" | jq

# Missing auth → 401
curl -si http://localhost:8095/api/orders -H "X-Tenant-Id: acme"

# Missing tenant → 400
curl -si http://localhost:8095/api/orders \
  -H "Authorization: Bearer $API_GATEWAY_DEMO_TOKEN"

# Rate limit (60/min per IP) — flood to see 429
for i in $(seq 1 80); do
  curl -s -o /dev/null -w "%{http_code}\n" http://localhost:8095/api/orders \
    -H "Authorization: Bearer $API_GATEWAY_DEMO_TOKEN" \
    -H "X-Tenant-Id: acme"
done | sort | uniq -c
```

## Smoke tests

```bash
go test ./examples/api-gateway/...
```

Covers:
- Happy path through the full chain.
- Unauthorized request does NOT reach the downstream (assertion
  on a counter inside an injected fake).
- Retry recovers from a single transient downstream failure.
- Breaker opens after sustained downstream failure (503 instead
  of 502 — distinct status codes for "rejected by breaker" vs
  "downstream returned error").
- Missing `X-Tenant-Id` header → 400 contract.
- Bulkhead full while a flood of in-flight calls saturates the
  cap → 503 with the "bulkhead full" message.
- Downstream slower than the request budget → 504 (and the gateway
  cancels the downstream before its natural completion).

## Production wiring

The example already uses `app.Builder`, opting out of TLS and the
Builder-level rate limit per-policy with inline kit-doctor:allow
markers (curl/test convenience). Production wires the real
infrastructure modules instead:

```go
import (
    "github.com/bds421/rho-kit/app/v2"
    "github.com/bds421/rho-kit/app/jwt/v2"
    "github.com/bds421/rho-kit/app/ratelimit/v2"
)

cfg, _ := app.LoadBaseConfig(8095)
builder := app.New("api-gateway", version, cfg).
    With(jwt.Module(jwksURL,
        jwt.WithIssuer(issuer),
        jwt.WithAudience(audience),
    )).
    With(ratelimit.IP(60, time.Minute)).
    With(ratelimit.Keyed("tenant", 1000, time.Minute)).
    Router(func(infra app.Infrastructure) http.Handler {
        // wire the downstream-fanout handler here, with the SAME
        // timeoutbudget → bulkhead → breaker → retry chain the
        // example demonstrates.
        return mux
    })
builder.Run() // installs the always-on production-safety validator
```

`kit-doctor` flags `Builder.Run()` without an explicit rate-limit
declaration (`rate-limit-omission`, HIGH). The Builder also runs
the wave-128 validator at startup that rejects empty TLS, missing
issuer/audience, exposed internal-host, weak sslmode, and
excessive tracing sample rates.

For bulkhead observability, wire `bulkhead.WithMetrics` against
the kit's Prometheus registerer so the
`bulkhead_acquisitions_total{name,outcome}` and
`bulkhead_acquire_duration_seconds{name,outcome}` series surface
on the operator dashboards.

## What's NOT in this example

- **Real JWT validation.** The stub uses a static bearer token;
  production wires `security/jwtutil` or `app/jwt` with a real
  JWKS endpoint.
- **Persistent rate-limit store.** `ratelimit.NewLimiter` is
  in-memory; production wraps `data/ratelimit/redis` for
  cross-replica sharing.
- **Tenant-keyed rate limit.** The example shows IP-keyed only;
  the production recipe combines `ratelimit.IP` and
  `ratelimit.Keyed` for per-tenant overlays.
- **Downstream fan-out.** The stub returns a synthetic string;
  production calls a gRPC client (wrapped with
  `grpcx/interceptor.StreamIdleTimeout`,
  `grpcx/interceptor.MaxConcurrentStreamsServer`) or an HTTP
  client from `httpx.NewHTTPClient`.
- **Observability.** Production wiring registers metrics and
  OTel spans automatically through the `app.Builder` and the
  middleware-level options (`ratelimit.WithMetrics`,
  `jwt.WithRegisterer`).
