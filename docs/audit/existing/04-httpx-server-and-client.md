# httpx/ — server, client, pagination, httpxtest

Server defaults, client defaults, and the test helpers. Middleware findings live in [05-httpx-middleware.md](05-httpx-middleware.md).

## Landed

- ✅ **`http.Server.ErrorLog` defaults to slog adapter** — TLS handshake errors no longer leak to stderr in unstructured form (commit `36cf34b`). New `WithErrorLog` option for callers that want their own log.
- ✅ **Client `MaxIdleConnsPerHost` raised to 100** — Go's stdlib default of 2 caused perf cliff for high-RPS callers (commit `36cf34b`).
- ✅ **`DecodeJSON` strict trailing-data rejection** — second `Decode` call requires `io.EOF`, so `{} {}` payloads no longer silently drop the second value (commit `36cf34b`).
- ✅ **Auditlog gormstore cursor signing** — auditlog cursor is now HMAC-signed (commit `98f05e4`); covers part of the broader pagination concern raised here.

## Open

### [HIGH] Cursor pagination is not opaque — leaks PKs and trusts client cursors
**File**: `httpx/pagination/cursor.go:43-78`
**Issue**: `BuildResult` returns the raw last-PK as `next_cursor`. `ParseCursorParams` reads `q.Get("cursor")` and `HandleCursorList` passes it to `ListFn(ctx, cursor, limit)`. Default validator only checks UUID syntax. A client can: (1) enumerate by guessing IDs; (2) skip access-control by passing a foreign user's ID as cursor; (3) inject SQL fragments if `ListFn` interpolates without parameterization.
**Fix**: Sign the cursor with HMAC at the kit level — `nextCursor = base64(id || hmac(secret, id || user_id))` — and verify on parse. Mirrors the auditlog gormstore pattern shipped in `98f05e4`. Document the SQL-injection risk loudly.
**Effort**: M
**Migration**: Phase 3. Existing cursors will not validate after the change; document the rollover (accept both formats for one release, then strict).

### [HIGH] `WriteTimeout` 35s but `Default` middleware doesn't include `Timeout`
**File**: `httpx/httpx.go:71` + `httpx/middleware/stack/stack.go`
**Issue**: Comment says `WriteTimeout` must exceed configured request timeout so middleware can write 503 — but `stack.Default` doesn't include the timeout middleware. So a slow handler is killed by `WriteTimeout` (server-level, generic message). Worse: if a user adds `timeout.Timeout(60*time.Second)` (longer than 35s `WriteTimeout`), the server kills the connection before middleware can write 503.
**Fix**: Either include `timeout.Timeout` in `Default` (sensible default 30s), or add startup validation that `WriteTimeout > timeoutMW + buffer`. Document the constraint.
**Effort**: S
**Phase**: 1

### [MEDIUM] Client doesn't expose `IdleConnTimeout` knob
**File**: `httpx/httpx.go:24-46`
**Issue**: `MaxIdleConnsPerHost` is now sane (default 100), but `IdleConnTimeout` still inherits the stdlib default (90s) with no option to change it. For services behind aggressive load balancers (60s idle), connections die mid-request.
**Fix**: Expose `WithIdleConnTimeout(time.Duration)` matching the existing `WithErrorLog` option pattern.
**Effort**: S

### [MEDIUM] `httpxtest` bypasses real `http.Server` — tests pass that fail in prod
**File**: `httpx/httpxtest/httpxtest.go:22-46`
**Issue**: `Do`/`DoRequest` call `handler.ServeHTTP` directly with `httptest.NewRequest`. `MaxHeaderBytes`, `RemoteAddr`, default `Host` are all fake. Middleware that asserts on these passes tests but fails prod.
**Fix**: Document loudly. Add a `DoRealServer(handler)` variant that uses `httptest.NewServer` so the full stack runs end-to-end.
**Effort**: S

### Migration checklist

- [ ] Phase 1: include `Timeout` in `stack.Default` (or validate WriteTimeout sizing).
- [ ] Phase 2: expose `WithIdleConnTimeout` on the default client.
- [ ] Phase 3: cursor pagination signing (mirror auditlog gormstore).
- [ ] Phase 3: `httpxtest.DoRealServer` variant.

### Related new packages

- [new/17-httpx-problem-details.md](../new/17-httpx-problem-details.md) — RFC 7807 alternative writer.
