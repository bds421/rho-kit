# observability/ — auditlog, health, logattr, logging, promutil, slo, tracing

## Landed

- ✅ **Tracing default sample rate dropped to 0.05** — was 1.0, which is wrong-shape for a kit-level default; collector + storage cost goes from impossible to budgeted (commit `1198dd5`).
- ✅ **Tracing Baggage opt-in only** — `Config.EnableBaggage` gates the `propagation.Baggage{}` propagator; default is TraceContext only, eliminating accidental PII propagation across services (commit `1198dd5`).
- ✅ **auditlog gormstore composite cursor** — pagination predicate is now `(timestamp, id) DESC` so events with identical microseconds don't get skipped or duplicated across page boundaries (commit `1198dd5`).
- ✅ **auditlog gormstore LIKE escape** — Resource filter uses `LIKE ? ESCAPE '\'` with `%` / `_` / `\` escaped in caller input (commit `1198dd5`).
- ✅ **auditlog gormstore signed cursors** — base64url(payload).base64url(HMAC); `decodeCursor` returns `ErrCursorInvalid` on tamper or cross-secret cursor; `WithCursorSecret` lets multi-replica deployments share signing key (commit `98f05e4`).

## Open

### [MEDIUM] `observability/health` provides no `/live`/`/ready` HTTP handlers
**File**: `observability/health/doc.go:1` + `healthcheck_cli.go:20`
**Issue**: doc.go advertises "readiness and liveness handlers"; the package only exposes `Checker.Evaluate`. Liveness vs readiness distinction (k8s pattern) is left to consumers.
**Fix**: Provide `Liveness()` and `Readiness(*Checker)` `http.Handler` constructors. Liveness returns 200 unconditionally; Readiness returns 503 when any critical dependency is unhealthy.
**Effort**: S
**Phase**: 3

### [MEDIUM] `logattr` has no PII/secret-redaction helpers
**File**: `observability/logattr/logattr.go`
**Issue**: Constructors for `UserID`, `Path`, `URL`, `RequestID` but nothing for "redact-this-string" or secret value. Logging `Authorization` headers, JWTs, password fields goes through `slog.String`/`slog.Any` with no protection.
**Fix**: Add `Secret(key, value string) slog.Attr` that emits `<redacted-N-bytes-hash:abc12>` instead of raw. Add `Email(addr)` that masks the local part.
**Effort**: S

### [MEDIUM] Tracing `Init` blocks on OTLP dial without timeout
**File**: `observability/tracing/tracing.go:85-87`
**Issue**: `otlptracegrpc.New(ctx, opts...)` performs an initial dial. If the collector is unreachable at startup and the caller passes a long-lived ctx, the service hangs during boot.
**Fix**: Wrap with `context.WithTimeout(ctx, 5*time.Second)` (or accept a Config field). On timeout fall back to noop provider with a logged warning.

### [MEDIUM] `MemoryStore.matchesFilter` ignores `Filter.IPAddress`
**File**: `observability/auditlog/memory.go:87-104`
**Issue**: Filter has `IPAddress` field that the SQL store filters on; in-memory store silently ignores it. Tests using MemoryStore pass with filters that fail in production.
**Fix**: Add the `IPAddress` check.

### [LOW] `promutil.RegisterCollector` panics on conflict; swallows AlreadyRegistered silently
**File**: `observability/promutil/register.go:16-23`
**Fix**: Return `(reused bool, err error)` so callers can decide. Or at least log a debug line on `AlreadyRegisteredError`.

### [LOW] SLO `evaluateLatency` aggregates buckets across all label combinations
**File**: `observability/slo/slo.go:296-319`
**Fix**: Add a `LabelFilter` to `SLO` for latency type; skip metrics whose labels don't match.

### Migration checklist

- [ ] Phase 3: `observability/health` ship Liveness/Readiness handlers.
- [ ] Phase 3: `logattr.Secret` + `Email` helpers.
- [ ] Phase 3: tracing `Init` timeout + noop fallback.
- [ ] Phase 3: auditlog memory IPAddress filter; promutil register semantics; SLO label filter.

### Related new packages

- [new/15-observability-pprof-runtime.md](../new/15-observability-pprof-runtime.md) — pprof + runtime metrics on internal port.
- [new/16-observability-red-metrics.md](../new/16-observability-red-metrics.md) — RED middleware with proper buckets.
