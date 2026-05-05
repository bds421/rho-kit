# NEW: httpx/middleware/recover

**Phase**: 1 (CRITICAL — closes the no-recover gap in `stack.Default`)
**Module path**: `github.com/bds421/rho-kit/httpx/middleware/recover`

## Why

Today `httpx/middleware/stack.Default` chains secheaders → metrics → requestid → correlationid → tracing → reqlogger → logging → handler. **No recover middleware exists in the kit at all** (`grep -r recover httpx/middleware/` returns zero matches outside cleanup goroutines). A handler panic relies on Go's stdlib `http.Server` recover, which logs to `ErrorLog` (also unset → stdout) with no JSON body, no request-ID correlation, no metric.

## Public API

```go
package recover

// Option configures the recover middleware.
type Option func(*config)

// WithLogger overrides the default slog.Default() logger.
func WithLogger(*slog.Logger) Option

// WithStatusCode overrides the response status (default 500).
func WithStatusCode(int) Option

// WithBody overrides the response body builder. Default builder writes
// {"error":"internal server error","code":"INTERNAL","request_id":"..."}.
func WithBody(func(r *http.Request, panicValue any) []byte) Option

// WithMetrics enables Prometheus counters for panics by status/route.
func WithMetrics(reg prometheus.Registerer) Option

// WithStackTrace enables capturing and logging stack traces (default: enabled).
func WithStackTrace(bool) Option

// Middleware returns a panic-recovery middleware.
func Middleware(opts ...Option) func(http.Handler) http.Handler
```

Behavior:
1. `defer recover()` around `next.ServeHTTP`.
2. On panic: log at error level with `request_id` (from context), URL, method, panic value, stack trace.
3. Skip if `http.ErrAbortHandler` (Go's signal that the handler aborted intentionally).
4. Don't write headers if they were already written (use a small response-recorder to detect; otherwise log a warning that the panic occurred mid-response).
5. Increment a `http_panics_total{route,status}` counter.

## Integration with `stack.Default`

```go
func Default(handler http.Handler, opts ...DefaultOption) http.Handler {
    cfg := buildDefault(opts...)
    return Chain(handler,
        recover.Middleware(recover.WithLogger(cfg.logger), recover.WithMetrics(cfg.registerer)), // OUTERMOST
        secheaders.Middleware(...),
        metrics.Middleware(...),
        requestid.Middleware(),
        correlationid.Middleware(),
        tracing.Middleware(...),
        logging.Middleware(...),
    )
}
```

`recover` must be the outermost layer so that panics in any other middleware are caught. Place it *before* secheaders so secheaders are still applied to the panic response.

## Definition of done

- [ ] Package created with the API above.
- [ ] `stack.Default` updated to include recover as outermost layer.
- [ ] Tests: panic in handler → 500 + structured JSON + log entry + metric increment + stack trace logged.
- [ ] Tests: `http.ErrAbortHandler` not logged or counted.
- [ ] Tests: panic in another middleware (e.g., logging) is recovered (recover is outermost).
- [ ] Tests: panic after WriteHeader → log warning, no double-write.
- [ ] Doc updated in `docs/ai/http.md`.
