# httpx/ — server, client, pagination, httpxtest

Server defaults, client defaults, and the test helpers. Middleware findings live in [05-httpx-middleware.md](05-httpx-middleware.md).

## Landed

- ✅ **`http.Server.ErrorLog` defaults to slog adapter** — TLS handshake errors no longer leak to stderr in unstructured form (commit `36cf34b`). New `WithErrorLog` option for callers that want their own log.
- ✅ **Client `MaxIdleConnsPerHost` raised to 100** — Go's stdlib default of 2 caused perf cliff for high-RPS callers (commit `36cf34b`).
- ✅ **`DecodeJSON` strict trailing-data rejection** — second `Decode` call requires `io.EOF`, so `{} {}` payloads no longer silently drop the second value (commit `36cf34b`).
- ✅ **Auditlog gormstore cursor signing** — auditlog cursor is now HMAC-signed (commit `98f05e4`); covers part of the broader pagination concern raised here.

## Open

_Closed — see Recently Landed below._

## Recently Landed (Phase 3, commit `6f03d78`)

- ✅ **Signed cursor pagination** — `CursorSigner` (`NewCursorSigner` / `MustNewCursorSigner`) HMAC-signs cursors as `base64url(payload).base64url(sig)`; `Encode` / `Decode` enforce constant-time verification; `CursorListOpts.Signer` opts handlers in. Mirrors the auditlog signing pattern.
- ✅ **`stack.Default` includes Timeout middleware** — 30s default with `WithTimeout(d)` override and `WithoutTimeout()` opt-out; sized so the server's 35s `WriteTimeout` always leaves middleware time to write 503. Constraint also explicitly documented on `WithWriteTimeout`.
- ✅ **`WithIdleConnTimeout(d)` on the client** — exposed through `NewTracingHTTPClientWithOptions(...)`.
- ✅ **`httpxtest.DoRealServer(t, handler, ...)`** — runs the full stack via `httptest.NewServer` so middleware sees real `RemoteAddr`, real `Host`, real `MaxHeaderBytes` enforcement.

### Migration checklist

- [x] Phase 1: include `Timeout` in `stack.Default`. ✅ `6f03d78`
- [x] Phase 2: expose `WithIdleConnTimeout` on the default client. ✅ `6f03d78`
- [x] Phase 3: cursor pagination signing. ✅ `6f03d78`
- [x] Phase 3: `httpxtest.DoRealServer` variant. ✅ `6f03d78`

### Related new packages

- [new/17-httpx-problem-details.md](../new/17-httpx-problem-details.md) — RFC 7807 alternative writer.
