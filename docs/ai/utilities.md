# Utilities — Errors, Validation, Pagination, Cache, Lifecycle, Concurrency

Packages: `core/apperror`, `core/validate`, `httpx/pagination`, `core/cache`, `runtime/lifecycle`, `core/contextutil`, `core/config`, `observability/logattr`, `io/atomicfile`, `io/progress`, `runtime/eventbus`

## apperror — Sum-Type Application Errors

Nine concrete error types, each carrying only their relevant fields. All implement the `apperror.AppError` interface (sealed — do not implement externally). Constructors return `error`, so consumer code doesn't need to know the concrete types:

```go
apperror.NewNotFound("user", id)                       // CodeNotFound → 404
apperror.NewValidation("invalid email")                 // CodeValidation → 400
apperror.NewFieldValidation(                            // CodeValidation → 400 with field details
    apperror.FieldError{Field: "email", Message: "must be valid"},
)
apperror.NewConflict("email already taken")             // CodeConflict → 409
apperror.NewAuthRequired("session expired")             // CodeAuthRequired → 401
apperror.NewForbidden("access denied")                  // CodeForbidden → 403
apperror.NewRateLimit("quota exceeded", 30*time.Second) // CodeRateLimit → 429 + Retry-After header
apperror.NewOperationFailed("payment declined")         // CodeOperationFailed → 500 (message exposed)
apperror.NewOperationFailedWithCause("failed", err)     // CodeOperationFailed → 500 (wraps cause)
apperror.NewPermanent("feature disabled")               // CodePermanent → 422 (skips retries)
apperror.NewPermanentWithCause("failed", err)           // CodePermanent → 422 (wraps cause)
apperror.NewUnavailable("not ready")                    // CodeUnavailable → 503 (self not ready)
apperror.NewUnavailableWithCause("down", err)           // CodeUnavailable → 503 (wraps cause)
apperror.NewDependencyUnavailable("redis", "msg", err)  // CodeUnavailable → 502 (upstream down)
```

Every error type implements `Retryable() bool`. Use `apperror.ShouldRetry` as a predicate for retry middleware:
```go
retry.Do(ctx, fn, retry.WithRetryIf(apperror.ShouldRetry))
```

Inspection (predicates use `errors.As` internally):
```go
apperror.IsNotFound(err)        // bool
apperror.IsValidation(err)      // bool
apperror.IsConflict(err)        // bool
apperror.IsAuthRequired(err)    // bool
apperror.IsForbidden(err)       // bool
apperror.IsRateLimit(err)       // bool
apperror.IsOperationFailed(err) // bool
apperror.IsPermanent(err)       // bool
apperror.IsUnavailable(err)     // bool

// Extract concrete types for field access:
if nf, ok := apperror.AsNotFound(err); ok { nf.Entity; nf.EntityID }
if ve, ok := apperror.AsValidation(err); ok { ve.Fields }
if rl, ok := apperror.AsRateLimit(err); ok { rl.RetryAfter }
if ue, ok := apperror.AsUnavailable(err); ok { ue.Dependency; ue.RetryAfter }

// HTTP status mapping (lives in httpx, not apperror — transport-agnostic):
status := httpx.HTTPStatus(err) // returns int

// Retry predicate (for resilience/retry integration):
apperror.ShouldRetry(err) // true for Conflict, RateLimit, Unavailable
```

**Key difference: `CodeOperationFailed` vs `CodeUnavailable`**: `OperationFailedError` indicates a server-side failure that is unlikely to resolve on retry (non-retryable). `UnavailableError` indicates a transient upstream failure that is worth retrying (retryable).

**Key difference: `CodeOperationFailed` vs untyped errors**: Both map to 500, but `CodeOperationFailed` exposes its message to the client (e.g., "payment declined"), while untyped errors get the generic "internal error" message to avoid leaking internals.

**Key difference: `CodeAuthRequired` vs middleware auth**: Middleware (`httpx/middleware/auth`) handles transport-level auth (JWT verification, mTLS). Use `CodeAuthRequired` for business-level auth failures from handler logic (e.g., expired session, revoked API key).

**Key difference: `CodeRateLimit` vs middleware ratelimit**: Middleware (`httpx/middleware/ratelimit`) handles IP/key-based rate limiting. Use `CodeRateLimit` for business-level quotas (e.g., monthly API call limit).

**Key integration**: `retry.RetryIfNotPermanent` skips retries for `CodePermanent`. Messaging consumers ACK permanently failed messages immediately. Use `apperror.ShouldRetry` as the retry predicate to integrate with `resilience/retry`.

**Not covered by apperror** — handled directly by middleware:
- **413 Payload Too Large** → `httpx/middleware/maxbody` rejects via `http.MaxBytesReader`

## validate — Struct Validation

Wraps `go-playground/validator`, returns `apperror.ValidationError` with JSON field names:

```go
type CreateUserRequest struct {
    Email string `json:"email" validate:"required,email"`
    Name  string `json:"name"  validate:"required,min=2,max=100"`
    Role  string `json:"role"  validate:"required,oneof=admin user viewer"`
    Age   int    `json:"age"   validate:"gte=0,lte=150"`
}

if err := validate.Struct(req); err != nil {
    httpx.WriteServiceError(w, r, logger, err)
    // {"error":"...", "code":"VALIDATION", "fields":[{"field":"email","message":"..."}]}
    return
}
```

**Handler pattern:**
```go
func createUser(w http.ResponseWriter, r *http.Request) {
    var req CreateUserRequest
    if !httpx.DecodeJSON(w, r, &req) { return }      // 400 on malformed JSON
    if err := validate.Struct(req); err != nil {       // 400 with field details
        httpx.WriteServiceError(w, r, logger, err)
        return
    }
    // proceed with validated req
}
```

Custom validators (register during init only):
```go
func init() {
    validate.RegisterValidation("slug", func(fl validator.FieldLevel) bool {
        return regexp.MustCompile(`^[a-z0-9-]+$`).MatchString(fl.Field().String())
    })
}
```

## pagination — Cursor-Based Pagination

UUID-cursor pagination with automatic `has_more` detection:

```go
func listUsers(w http.ResponseWriter, r *http.Request) {
    p := pagination.ParseCursorParams(r, 20, 100) // defaultLimit, maxLimit
    if err := pagination.ValidateCursorUUID(p.Cursor); err != nil {
        httpx.WriteError(w, 400, err.Error())
        return
    }

    // Fetch limit+1 to detect has_more:
    users, _ := repo.List(ctx, p.Cursor, p.Limit+1)

    result := pagination.BuildResult(users, p.Limit, func(u User) string {
        return u.ID // cursor value = primary key
    })
    httpx.WriteJSON(w, 200, result)
}
```

Response:
```json
{
    "data": [...],
    "next_cursor": "550e8400-e29b-41d4-a716-446655440000",
    "has_more": true
}
```

Query: `GET /users?limit=20&cursor=<next_cursor>`

## cache — In-Memory Cache

For single-instance caching without Redis:

```go
mc := cache.NewMemoryCache(
    cache.WithMaxSize(10_000),       // max entries
    cache.WithCleanupInterval(time.Minute),
)
defer mc.Close()

mc.Set(ctx, "key", []byte("value"), 5*time.Minute) // 0 TTL = no expiry
val, err := mc.Get(ctx, "key")
if errors.Is(err, cache.ErrCacheMiss) { /* cold */ }
mc.Delete(ctx, "key")
mc.Exists(ctx, "key")
```

### TypedCache (JSON)

```go
tc, _ := cache.NewTypedCache[User](mc, "users:")
tc.Set(ctx, "123", user, time.Minute)
user, err := tc.Get(ctx, "123") // deserializes from JSON
```

Both `MemoryCache` and `TypedCache` implement `cache.Cache`. For distributed caching, use `data/cache/rediscache` (same interface).

## lifecycle — Composable Service Lifecycle

The `lifecycle.Runner` manages concurrent components with coordinated shutdown. Used internally by `app.Builder.Run()` and available for custom setups:

```go
runner := lifecycle.NewRunner(logger, lifecycle.WithStopTimeout(30*time.Second))
runner.Add("http", lifecycle.HTTPServer(srv))
runner.AddFunc("worker", func(ctx context.Context) error {
    worker.Run(ctx)
    return nil
})
runner.AddFunc("metrics", func(ctx context.Context) error {
    exportMetrics(ctx)
    return nil
})

err := runner.Run(context.Background()) // blocks until signal or error
```

Components implement `lifecycle.Component` (Start + Stop). If any component returns an error, all others are cancelled. On SIGINT/SIGTERM, all components are stopped in reverse registration order. The Runner includes panic recovery per goroutine.

## contextutil — Typed Context Keys

Generic type-safe context keys (replaces `context.WithValue` with string keys):

```go
type UserID string  // named type for distinct keying
var userIDKey contextutil.Key[UserID]

ctx = userIDKey.Set(ctx, "user-123")
id, ok := userIDKey.Get(ctx)     // ("user-123", true)
id = userIDKey.MustGet(ctx)      // panics if not set
```

Keys are distinguished by their type parameter. Two `Key[string]` share the same slot — use named types (`type UserID string`) for distinct keys of the same underlying type.

## config — Struct-Tag Config Loading

Load environment variables into structs using tags:

```go
type Config struct {
    Host     string        `env:"DATABASE_HOST,required"`
    Port     int           `env:"DATABASE_PORT" default:"5432"`
    Timeout  time.Duration `env:"TIMEOUT" default:"30s"`
    Debug    bool          `env:"DEBUG" default:"false"`
    Password string        `env:"DB_PASSWORD" secret:"true"` // supports _FILE suffix
    Tags     []string      `env:"TAGS"`                       // comma-separated
}

cfg, err := config.Load[Config]()    // returns error on validation failure
cfg := config.MustLoad[Config]()     // panics on error (for main())
```

Supports: string, int, int64, uint, uint16, bool, float64, time.Duration, []string, *url.URL. Nested structs are recursed automatically.

## atomicfile — Safe File Writes

Write-then-rename to prevent partial writes:

```go
err := atomicfile.WriteFile(path, data, 0644)
// Writes to temp file in same dir, then os.Rename (atomic on same filesystem)
```

## ioutil — Reader Wrappers

```go
// Progress tracking:
pr := progress.NewProgressReader(reader, totalSize, func(bytesRead, total int64) {
    fmt.Printf("%.1f%%\n", float64(bytesRead)/float64(total)*100)
})

// Bandwidth throttling:
tr := progress.NewThrottledReader(reader, 1<<20) // 1 MiB/s
```

## logattr — Structured Log Field Schema

Standard `slog.Attr` constructors for consistent field names across the kit:

```go
import "github.com/bds421/rho-kit/observability/logattr"

logger.Error("request failed",
    logattr.Error(err),
    logattr.RequestID(reqID),
    logattr.Method(r.Method),
    logattr.Path(r.URL.Path),
    logattr.StatusCode(500),
)

logger.Info("component starting", logattr.Component("http"))
logger.Warn("retrying", logattr.Attempt(3), logattr.Delay(5*time.Second))
logger.Info("listening", logattr.Addr(":8080"))
logger.Info("connected", logattr.Instance("cache"))
```

Available: `Error`, `Component`, `RequestID`, `Addr`, `Attempt`, `Delay`, `Method`, `Path`, `StatusCode`, `Instance`.

## eventbus — In-Process Domain Events

Type-safe publish/subscribe for domain events within a single process. For cross-service messaging, use `infra/messaging` (RabbitMQ). For durable event streaming, use `data/stream/redisstream`.

```go
// 1. Define an event (plain struct + EventName method):
type OrderPlaced struct {
    OrderID    string
    CustomerID string
    Total      float64
}
func (OrderPlaced) EventName() string { return "order.placed" }

// 2. Subscribe (inside RouterFunc using infra.EventBus):
eventbus.Subscribe(infra.EventBus, func(ctx context.Context, e OrderPlaced) error {
    return sendConfirmationEmail(ctx, e.OrderID)
}, eventbus.WithName("send-confirmation"))

// 3. Publish (from handler code):
err := eventbus.Publish(infra.EventBus, ctx, OrderPlaced{
    OrderID: "ord-123", CustomerID: "cust-456", Total: 99.99,
})
```

Async handlers (fire-and-forget, errors go to OnError callback):

```go
eventbus.Subscribe(infra.EventBus, func(ctx context.Context, e OrderPlaced) error {
    return updateAnalytics(ctx, e)
}, eventbus.WithAsync(), eventbus.WithName("analytics"))
```

**Key rules:**
- `Subscribe` and `Publish` are package-level functions (Go methods can't have type params)
- Sync handlers: errors collected via `errors.Join` and returned from `Publish`
- Async handlers: errors logged and sent to `WithOnError` callback; panics recovered
- Always available on `infra.EventBus` (no `WithEventBus()` needed)
- NOT for cross-service communication (use `infra/messaging` instead)

## Anti-Patterns

- **Never** use string errors for validation — use `apperror.NewValidation` or `apperror.NewFieldValidation`.
- **Never** return generic `errors.New()` from handlers — use `core/apperror` types for automatic HTTP mapping.
- **Never** skip `ValidateCursorUUID` — unvalidated cursors can cause SQL injection.
- **Never** forget `limit+1` when fetching for cursor pagination — `BuildResult` needs it for `has_more`.
- **Never** use `cache.MemoryCache` for data that must be shared across instances — use `data/cache/rediscache`.
- **Never** register custom validators after init — `validate.RegisterValidation` is not concurrent-safe.
