# HTTP — Server, Middleware, JSON Helpers

Packages: `httpx`, `httpx/middleware/stack`, `httpx/middleware/auth`, `httpx/middleware/csrf`, `httpx/middleware/cors`, `httpx/middleware/ratelimit`, `httpx/middleware/budget`, `httpx/budget`, `httpx/middleware/logging`, `httpx/middleware/metrics`, `httpx/middleware/requestid`, `httpx/middleware/timeout`, `httpx/middleware/clientip`, `httpx/middleware/maxbody`, `httpx/middleware/idempotency`

Snippet status: Go blocks in this recipe are illustrative fragments unless
explicitly introduced as generated or executable code. Buildable golden-path
evidence lives in `cmd/kit-new` scaffold tests and `examples/agentic-service`.

## When to Use

Every HTTP service uses `stack.Default()` for the canonical middleware chain. Use `httpx` for server creation, JSON encoding/decoding, and error responses. Use individual middleware packages for specific concerns.

## Quick Start

```go
Router(func(infra app.Infrastructure) http.Handler {
    mux := http.NewServeMux()
    mux.HandleFunc("GET /users/{id}", getUser(infra))
    mux.HandleFunc("POST /users", createUser(infra))
    csrfMW := csrf.New(
        csrf.WithSecret(cfg.CSRFSecret),
        csrf.WithAllowedOrigins(cfg.PublicOrigin),
    )

    return stack.Default(mux, infra.Logger,
        stack.WithOuter(csrfMW, csrf.RequireJSONContentType),
        stack.WithInner(auth.RequireUserWithJWT(infra.JWT)),
    )
})
```

## Middleware Stack Order

`stack.Default()` applies middleware outer-to-inner:

```
[Outer] → metrics → requestID → tracing → logging → [Inner] → handler
```

- **Outer**: Rate limiting, CSRF — reject early before any work
- **Inner**: Auth, permissions — closest to the handler
- **Logging**: Access, request-scoped, panic-recovery, and service-error logs
  redact request path values by default. Use route-pattern metrics/traces or
  explicit application attributes for intentionally non-sensitive route names.

```go
stack.Default(mux, logger,
    stack.WithOuter(
        infra.RateLimiter.Middleware,
        csrf.New(
            csrf.WithSecret(cfg.CSRFSecret),
            csrf.WithAllowedOrigins(cfg.PublicOrigin),
        ),
        csrf.RequireJSONContentType,
    ),
    stack.WithInner(
        auth.RequireUserWithJWT(infra.JWT),
        auth.RequirePermission("users:read"),
    ),
    stack.WithQuietPaths("/ready", "/health"), // logged at Debug level
)
```

## Authentication Recipes

```go
// JWT-only:
auth.RequireUserWithJWT(infra.JWT)

// Service-to-service (JWT OR mTLS):
auth.RequireS2SAuth(infra.JWT, []string{"payment-service", "order-service"},
    auth.WithS2SImpersonationGuard(func(r *http.Request, identity, userID string) error {
        return authorizeServiceUser(r.Context(), identity, userID)
    }),
)

// RBAC after auth:
auth.RequirePermission("orders:write")

// Method-aware RBAC (GET=read, POST/PUT/DELETE=write):
auth.PermissionByMethod("orders:read", "orders:write")

// API key scopes:
auth.RequireScope("api:write")       // soft — session auth passes through
auth.RequireScopeStrict("api:write") // strict — rejects if no scopes present

// Access user in handler:
userID := auth.UserID(r.Context())
perms := auth.Permissions(r.Context()) // nil for mTLS S2S
```

Identity-bearing headers such as `X-User-Id`, tenant headers, MCP `X-Actor-Id`,
and approval actor headers are treated as singleton tokens: duplicate lines,
comma-combined values, whitespace, and control characters are rejected or
rejected before audited work runs unless a middleware documents an explicit
opt-out. Do not parse identity headers with `Header.Get`; use verified context
values where possible, or the kit's explicit trusted-header helpers.
`X-Request-Id` and `X-Correlation-Id` are also singleton safe tokens. Values
outside the kit correlation-token alphabet are replaced instead of echoed into
response headers or propagated to downstream calls.

## Rate Limiting Recipes

```go
// IP-based (via Builder):
app.New(...).WithIPRateLimit(100, time.Minute)
// then: stack.WithOuter(infra.RateLimiter.Middleware)

// Keyed per-user (via Builder):
app.New(...).WithKeyedRateLimit("api", 10, time.Second)
// then in router:
mux.Handle("/api/", ratelimit.KeyedRateLimitMiddleware(
    infra.KeyedLimiters["api"],
    func(r *http.Request) string { return auth.UserID(r.Context()) },
)(apiHandler))
```

Both return `429` with `Retry-After` header.

Builder-managed rate limiters register their cleanup loops automatically. When
constructing `ratelimit.NewRateLimiter` or `NewKeyedRateLimiter` manually, run
exactly one cleanup loop per limiter and propagate its error:

```go
go func() {
    if err := limiter.Run(ctx); err != nil {
        logger.Error("rate limiter cleanup stopped", "err", err)
    }
}()
```

`Run` rejects nil contexts, uninitialized limiters, and duplicate starts.

Builder-created IP and keyed rate limiters are wired with these metrics
automatically. Manual rate limiters expose the same stable Prometheus counters
and retry-after histograms:

```go
metrics := ratelimit.NewMetrics(prometheus.DefaultRegisterer)

limiter := ratelimit.NewRateLimiter(100, time.Minute,
    ratelimit.WithMetrics(metrics),
    ratelimit.WithLimiterName("public_api"),
)

keyed := ratelimit.NewKeyedRateLimiter(10, time.Second,
    ratelimit.WithKeyedMetrics(metrics),
    ratelimit.WithKeyedLimiterName("api_key"),
)
```

Metric contract:

- `http_ratelimit_decisions_total{limiter,kind,outcome}`
- `http_ratelimit_retry_after_seconds{limiter,kind}`

`kind` is `ip` or `keyed`. Outcomes are `allowed`, `limited`,
`invalid_client_ip`, `invalid_key`, `unavailable`, `degraded_passthrough`, and
`degraded_rejected`. `limiter` must be a static low-cardinality name; never
use a raw IP, API key, tenant ID, user ID, or route parameter.

## Budget Recipes

Use `httpx/middleware/budget` for inbound per-request cost budgets and
`httpx/budget` for outbound provider-spend accounting. Tenant-scoped inbound
budgets require tenant middleware to run first; the default key function fails
closed with `400` when no tenant key is present. Use
`budgetmw.WithAllowMissingKey()` only on routes where missing budget attribution
is intentional, such as public health checks. The Redis budget backend uses
Lua for atomic accounting, so configure caps at or below `9_007_199_254_740_991`
units.

```go
tenantBudget := budgetredis.New(redisClient, 1_000_000, time.Hour)

return stack.Default(mux, logger,
    stack.WithOuter(
        tenant.New(tenant.WithRequired(true)),
        budgetmw.Middleware(tenantBudget,
            budgetmw.WithScope("tokens-per-hour"),
        ),
    ),
)
```

Outbound wrappers pre-charge before dispatch and, when configured, reconcile
against an authoritative response header. Transport errors retain the
pre-charge by default because a timeout or broken connection can happen after a
paid upstream has already done work; add `httpbudget.WithRefundOnTransportError()`
only for providers that guarantee failed requests are never billed.

```go
client := &http.Client{
    Transport: httpbudget.Wrap(http.DefaultTransport, tenantBudget, tenantID,
        httpbudget.WithEstimateHeader("X-Estimated-Tokens"),
        httpbudget.WithActualHeader("X-Actual-Tokens"),
    ),
}
```

## JSON Request/Response

```go
// Decode with 1MB limit, JSON Content-Type check, and error response on failure:
var req CreateUserRequest
if !httpx.DecodeJSON(w, r, &req) { return } // writes 415 on non-JSON, 400 on malformed or invalid requests

// Respond with JSON:
httpx.WriteJSON(w, http.StatusOK, user)

// Error response (structured {error, code}):
httpx.WriteError(w, http.StatusNotFound, "user not found")
// Error JSON responses include Cache-Control: no-store.

// Map apperror types to HTTP status automatically:
httpx.WriteServiceError(w, r, logger, err)
// NotFound→404, Validation→400, Conflict→409, AuthRequired→401,
// RateLimit→429 (+Retry-After), OperationFailed→500 (generic body),
// Permanent→422, else→500 (generic "internal error")

// Validation errors with field details:
httpx.WriteValidationError(w, logger, err)
// {"error":"...", "code":"VALIDATION", "fields":[{"field":"email","message":"..."}]}
```

Use `httpx/problemdetails` for RFC 7807 responses exposed to third-party
clients:

```go
problemdetails.Write(w, problemdetails.FromError(err,
    problemdetails.WithBaseURL("https://errors.example.com/docs"),
    problemdetails.WithInstance(r.URL.EscapedPath()),
))
```

`WithBaseURL` accepts only absolute `http`/`https` documentation roots with a
host and no credentials, query, or fragment components.
`WithInstance` requires path-only values such as `r.URL.EscapedPath()`;
query strings often carry OAuth codes, reset tokens, or presigned data and
should not be reflected into error bodies.

## HTTP Server & Client

```go
// Server with safe defaults (15s read, 5s header, 35s write, 60s idle, 1MB headers).
// app.Builder wires ErrorLog automatically; manual servers should pass the service logger.
serverLog := slog.NewLogLogger(logger.Handler(), slog.LevelWarn)
srv := httpx.NewServer(":8080", handler, httpx.WithErrorLog(serverLog))
wsSrv := httpx.NewServer(":8080", handler, httpx.WithErrorLog(serverLog), httpx.WithWriteTimeout(0)) // WebSocket

// HTTP client (always use instead of http.DefaultClient):
client := httpx.NewHTTPClient(10*time.Second, tlsConfig) // nil TLS = no mTLS
client := httpx.NewTracingHTTPClient(10*time.Second, tlsConfig) // with OpenTelemetry
client := httpx.NewHTTPClient(10*time.Second, tlsConfig, httpx.WithFollowRedirects(3)) // explicit redirect opt-in
client := httpx.NewResilientHTTPClient(httpx.WithResilientIdleConnTimeout(30*time.Second)) // circuit breaker + kit transport defaults

// infra.HTTPClient is pre-configured (tracing-aware if WithTracing used)
```

Kit-created outbound HTTP clients block redirects by default and return
`httpx.ErrRedirectBlocked`. Enable redirects only when the downstream contract
requires them, and always use a small hop limit with `WithFollowRedirects` or
`WithResilientFollowRedirects`.
`WithTLSConfig` and `WithResilientTLS` snapshot caller-owned TLS configs when
the option is created; later mutation of the original `*tls.Config` does not
change the server or resilient client.

## Safe Redirects

Validate untrusted redirect targets before passing them to `http.Redirect`.
Relative targets stay local; absolute targets must match an explicit allowlist.

```go
target, err := httpx.SafeRedirect(r.URL.Query().Get("next"), "accounts.example.com")
if err != nil {
    target = "/"
}
http.Redirect(w, r, target, http.StatusSeeOther)
```

`SafeRedirect` rejects scheme-relative URLs (`//evil.example`), encoded
scheme-relative paths, userinfo, backslashes, control bytes, non-HTTP schemes,
malformed percent escapes, and absolute hosts outside the allowlist.

Use `httpx/urlutil.AppendPaths` for static URL construction where each dynamic
part must remain one path segment. It preserves harmless existing percent
escapes, but re-encodes encoded separators and dot segments so caller-supplied
parts cannot smuggle extra path levels.

## Request ID

The `httpx/middleware/requestid` package sets a unique request ID on every request (from `X-Request-Id` header or auto-generated). Extract it in handler code via `httpx`:

```go
reqID := httpx.RequestID(r.Context()) // lives in httpx, not middleware/requestid
```

The middleware stores the ID using `httpx.SetRequestID(ctx, id)`, so retrieval is always via `httpx.RequestID(ctx)`.

## Other Middleware

```go
// CSRF (defense-in-depth, use both):
csrf.New(
    csrf.WithSecret(cfg.CSRFSecret),
    csrf.WithAllowedOrigins(cfg.PublicOrigin),
)                           // double-submit cookie + HMAC token validation
csrf.RequireJSONContentType // rejects POST/PUT/PATCH with bodies without JSON Content-Type

// Request timeout (503 on expiry, skips WebSocket upgrades):
timeout.Timeout(30 * time.Second)

// Max body size (413 on excess):
maxbody.MaxBodySize(10 << 20) // 10 MiB

// CORS: install only on browser cross-origin APIs, with explicit origins.
// Omit the middleware entirely when no CORS API is exposed.
cors.New(cors.Options{
    AllowedOrigins: []string{"https://app.example.com"},
})

// Client IP (proxy-aware, for rate limiting):
ip := clientip.ClientIP(r) // trusts X-Real-IP, X-Forwarded-For from loopback peers only
```

## Typed Handlers (Reduce Boilerplate)

Generic handlers that auto-decode JSON, validate, call your function, and encode the response:

```go
// POST with JSON body — decodes, validates, encodes response:
httpx.Handle(mux, "POST /users", logger, func(ctx context.Context, r *http.Request, req CreateUserRequest) (UserResponse, error) {
    user, err := svc.Create(ctx, req)
    if err != nil { return UserResponse{}, err } // auto-mapped via apperror
    return toResponse(user), nil
})

// GET with no body — just returns a response:
httpx.HandleNoBody(mux, "GET /users/{id}", logger, func(ctx context.Context, r *http.Request) (UserResponse, error) {
    id := r.PathValue("id")
    return svc.GetByID(ctx, id)
})

// POST returning 201 Created — custom status code:
httpx.HandleStatus(mux, "POST /orders", logger, func(ctx context.Context, r *http.Request, req CreateOrderRequest) (int, OrderResponse, error) {
    order, err := svc.Create(ctx, req)
    if err != nil { return 0, OrderResponse{}, err }
    return http.StatusCreated, toOrderResponse(order), nil
})
```

All three automatically:
- Decode JSON with 1MB body limit
- Validate using `validate.Struct()` (returns 400 with field details)
- Map `core/apperror` types to HTTP status codes via `WriteServiceError`
- Encode success responses as JSON; error responses include `Cache-Control: no-store`

## Middleware Chains (Custom Ordering)

When `stack.Default()` doesn't fit, use `stack.Chain` for explicit composition:

```go
chain := stack.NewChain(
    middleware.Metrics,
    middleware.RequestID,
    middleware.Logging(logger),
)

// Immutable — Append returns a new chain:
withAuth := chain.Append(auth.RequireUserWithJWT(provider))

handler := withAuth.Then(mux)     // or .ThenFunc(fn)
chain.Len()                        // number of middlewares
```

## Idempotency Middleware

Deduplicates requests by `Idempotency-Key` header:

```go
store := idempotency.NewMemoryStore() // use redisstore.New or pgstore.New in production
mux.Handle("/api/", idempotency.Middleware(store,
    idempotency.WithTTL(24*time.Hour),           // default
    idempotency.WithHeader("Idempotency-Key"),   // default
    idempotency.WithRequiredMethods("POST", "PUT", "PATCH"), // default
)(apiHandler))
```

- GET/HEAD/OPTIONS pass through without requiring the header
- POST/PUT/PATCH without the header return 400
- First request: captures response, stores it, returns normally
- Subsequent requests with same key: replays cached response
- Cache write/unlock work after the handler uses a bounded detached context
  that preserves request values such as tenant, trace, and logger data while
  surviving caller cancellation

## Path Parameters

```go
// UUID path parameter with validation:
id, ok := httpx.ParsePathID(w, r, "id") // writes 400 on invalid UUID
if !ok { return }

// Uint path parameter:
id, ok := httpx.ParseID(r) // from {id} wildcard

// Partial update helper (only sets non-nil fields):
updates := make(map[string]any)
httpx.SetIfNotNil(updates, "name", req.Name)   // *string
httpx.SetIfNotNil(updates, "age", req.Age)      // *int
```

## Anti-Patterns

- **Never** create or serve with `net/http` server entrypoints (`http.Server{}`, `http.ListenAndServe`, `http.Serve`) directly — use `httpx.NewServer` for safe defaults.
- **Never** use `http.DefaultClient` — use `httpx.NewHTTPClient` or `infra.HTTPClient`.
- **Never** manually order middleware — use `stack.Default` with `WithOuter`/`WithInner`.
- **Never** pass `nil` JWT provider to `auth.RequireUserWithJWT` — it panics by design.
- **Never** use `r.URL.Path` for metrics labels — use `r.Pattern` (Go 1.22+) to avoid cardinality explosion.
