# httpx/ — server, client, pagination, httpxtest

Server defaults, client defaults, and the test helpers. Middleware findings live in [05-httpx-middleware.md](05-httpx-middleware.md).

### [HIGH] `http.Server.ErrorLog` unset → TLS handshake errors leak RemoteAddr to stdout
**File**: `httpx/httpx.go:65-79`
**Issue**: `NewServer` sets timeouts and header limits but not `ErrorLog`. Go's stdlib then writes TLS handshake failures and protocol errors to the standard `log` package, including the client's `RemoteAddr`. Operators shipping stdout to a SIEM get raw IPs unstructured into security logs.
**Fix**: Default `ErrorLog` to a `log.New` backed by an `slog.NewLogLogger` adapter. Expose `WithErrorLog(*log.Logger)` option.
**Effort**: S

### [HIGH] HTTP client defaults: `MaxIdleConnsPerHost = 2` (perf cliff)
**File**: `httpx/httpx.go:24-46` + `httpx/resilient.go:113`
**Issue**: `NewHTTPClient`/`NewTracingHTTPClient`/`NewResilientHTTPClient` clone `http.DefaultTransport` without overriding `MaxIdleConnsPerHost` (default 2) or `MaxConnsPerHost` (unlimited). For a service doing 1k RPS to one downstream, the client opens & closes connections continuously and bypasses keep-alive benefits.
**Fix**: Default `MaxIdleConnsPerHost: 100` (configurable). Expose `IdleConnTimeout` separately (default 90s).
**Effort**: S

### [HIGH] `WriteTimeout` 35s but `Default` middleware doesn't include `Timeout`
**File**: `httpx/httpx.go:71` + `httpx/middleware/stack/stack.go`
**Issue**: Comment says `WriteTimeout` must exceed configured request timeout so middleware can write 503 — but `stack.Default` doesn't include the timeout middleware. So a slow handler is killed by `WriteTimeout` (server-level, generic message). Worse: if a user adds `timeout.Timeout(60*time.Second)` (longer than 35s `WriteTimeout`), the server kills the connection before middleware can write 503.
**Fix**: Either include `timeout.Timeout` in `Default` (sensible default 30s), or add startup validation that `WriteTimeout > timeoutMW + buffer`. Document the constraint.
**Effort**: S

### [HIGH] `httpx.DecodeJSON` doesn't reliably reject trailing top-level JSON
**File**: `httpx/httpx.go:162`
**Issue**: After the first decode, the code calls `dec.More()` to reject trailing data. `More()` is meant for array/object iteration; it does not reliably detect a second top-level JSON value. Bodies like `{"name":"ok"} {"ignored":true}` are silently accepted despite the comment promising trailing data is rejected. Undermines strict-parsing guarantees in typed handlers.
**Fix**: After the first decode, attempt a second `dec.Decode(&struct{}{})` and require `io.EOF`. Preserve the existing `MaxBytesError` handling.
**Effort**: S
**Phase**: 1

### [HIGH] Cursor pagination is not opaque — leaks PKs and trusts client cursors
**File**: `httpx/pagination/cursor.go:43-78`
**Issue**: `BuildResult` returns the raw last-PK as `next_cursor`. `ParseCursorParams` reads `q.Get("cursor")` and `HandleCursorList` passes it to `ListFn(ctx, cursor, limit)`. Default validator only checks UUID syntax. A client can: (1) enumerate by guessing IDs; (2) skip access-control by passing a foreign user's ID as cursor; (3) inject SQL fragments if `ListFn` interpolates without parameterization.
**Fix**: Sign the cursor with HMAC at the kit level — `nextCursor = base64(id || hmac(secret, id || user_id))` — and verify on parse. Document the SQL-injection risk loudly.
**Effort**: M
**Migration**: Phase 3. Existing cursors will not validate after the change; document the rollover (accept both formats for one release, then strict).

### [MEDIUM] `httpxtest` bypasses real `http.Server` — tests pass that fail in prod
**File**: `httpx/httpxtest/httpxtest.go:22-46`
**Issue**: `Do`/`DoRequest` call `handler.ServeHTTP` directly with `httptest.NewRequest`. `MaxHeaderBytes`, `RemoteAddr`, default `Host` are all fake. Middleware that asserts on these passes tests but fails prod.
**Fix**: Document loudly. Add a `DoRealServer(handler)` variant that uses `httptest.NewServer` so the full stack runs end-to-end.
**Effort**: S

### Migration checklist

- [ ] Phase 1: `http.Server.ErrorLog` defaults to slog adapter.
- [ ] Phase 1: client `MaxIdleConnsPerHost` raised; expose `IdleConnTimeout`.
- [ ] Phase 1: include `Timeout` in `stack.Default` (or validate WriteTimeout sizing).
- [ ] Phase 1: `DecodeJSON` reject trailing top-level JSON (second-decode + EOF check).
- [ ] Phase 3: cursor pagination signing.
- [ ] Phase 3: `httpxtest.DoRealServer` variant.

### Related new packages

- [new/17-httpx-problem-details.md](../new/17-httpx-problem-details.md) — RFC 7807 alternative writer.
