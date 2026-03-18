# HTTP — Server, Middleware, JSON Helpers

Packages: `httpx`, `httpx/middleware/stack`, `httpx/middleware/auth`, `httpx/middleware/csrf`, `httpx/middleware/cors`, `httpx/middleware/ratelimit`, `httpx/middleware/logging`, `httpx/middleware/metrics`, `httpx/middleware/requestid`, `httpx/middleware/timeout`, `httpx/middleware/clientip`, `httpx/middleware/maxbody`, `httpx/middleware/idempotency`

## When to Use

Every HTTP service uses `stack.Default()` for the canonical middleware chain. Use `httpx` for server creation, JSON encoding/decoding, and error responses. Use individual middleware packages for specific concerns.

## Quick Start

```go
Router(func(infra app.Infrastructure) http.Handler {
    mux := http.NewServeMux()
    mux.HandleFunc("GET /users/{id}", getUser(infra))
    mux.HandleFunc("POST /users", createUser(infra))

    return stack.Default(mux, infra.Logger,
        stack.WithOuter(csrf.RequireCSRF, csrf.RequireJSONContentType),
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

```go
stack.Default(mux, logger,
    stack.WithOuter(
        infra.RateLimiter.Middleware,
        csrf.RequireCSRF,
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
// JWT-only (Oathkeeper tokens):
auth.RequireUserWithJWT(infra.JWT)

// Service-to-service (JWT OR mTLS):
auth.RequireS2SAuth(infra.JWT, []string{"payment-service", "order-service"})

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

## JSON Request/Response

```go
// Decode with 1MB limit + error response on failure:
var req CreateUserRequest
if !httpx.DecodeJSON(w, r, &req) { return } // writes 400 automatically

// Respond with JSON:
httpx.WriteJSON(w, http.StatusOK, user)

// Error response (structured {error, code}):
httpx.WriteError(w, http.StatusNotFound, "user not found")

// Map apperror types to HTTP status automatically:
httpx.WriteServiceError(w, r, logger, err)
// NotFound→404, Validation→400, Conflict→409, AuthRequired→401,
// RateLimit→429 (+Retry-After), OperationFailed→500 (msg exposed),
// Permanent→422, else→500 (generic "internal error")

// Validation errors with field details:
httpx.WriteValidationError(w, logger, err)
// {"error":"...", "code":"VALIDATION", "fields":[{"field":"email","message":"..."}]}
```

## HTTP Server & Client

```go
// Server with safe defaults (15s read, 5s header, 35s write, 60s idle, 1MB headers):
srv := httpx.NewServer(":8080", handler)
srv := httpx.NewServer(":8080", handler, httpx.WithWriteTimeout(0)) // WebSocket

// HTTP client (always use instead of http.DefaultClient):
client := httpx.NewHTTPClient(10*time.Second, tlsConfig) // nil TLS = no mTLS
client := httpx.NewTracingHTTPClient(10*time.Second, tlsConfig) // with OpenTelemetry

// infra.HTTPClient is pre-configured (tracing-aware if WithTracing used)
```

## Request ID

The `httpx/middleware/requestid` package sets a unique request ID on every request (from `X-Request-Id` header or auto-generated). Extract it in handler code via `httpx`:

```go
reqID := httpx.RequestID(r.Context()) // lives in httpx, not middleware/requestid
```

The middleware stores the ID using `httpx.SetRequestID(ctx, id)`, so retrieval is always via `httpx.RequestID(ctx)`.

## Other Middleware

```go
// CSRF (defense-in-depth, use both):
csrf.RequireCSRF             // rejects mutating requests without X-Requested-With
csrf.RequireJSONContentType  // rejects POST/PUT/PATCH without application/json

// Request timeout (503 on expiry, skips WebSocket upgrades):
timeout.Timeout(30 * time.Second)

// Max body size (413 on excess):
maxbody.MaxBodySize(10 << 20) // 10 MiB

// Client IP (proxy-aware, for rate limiting):
ip := clientip.ClientIP(r) // trusts X-Real-IP, X-Forwarded-For from private ranges
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
- Encode response as JSON with `Cache-Control: no-store`

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
store := idempotency.NewMemoryStore() // or implement idempotency.Store for Redis
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

- **Never** create `http.Server{}` directly — use `httpx.NewServer` for safe defaults.
- **Never** use `http.DefaultClient` — use `httpx.NewHTTPClient` or `infra.HTTPClient`.
- **Never** manually order middleware — use `stack.Default` with `WithOuter`/`WithInner`.
- **Never** pass `nil` JWT provider to `auth.RequireUserWithJWT` — it panics by design.
- **Never** use `r.URL.Path` for metrics labels — use `r.Pattern` (Go 1.22+) to avoid cardinality explosion.
