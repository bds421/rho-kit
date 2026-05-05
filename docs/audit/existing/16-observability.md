# observability/ — auditlog, health, logattr, logging, promutil, slo, tracing

### [HIGH] Tracing default sample rate is 100%
**File**: `observability/tracing/tracing.go:69-71`
**Issue**: `if cfg.SampleRate <= 0 { cfg.SampleRate = 1.0 }`. Toolkit-level defaults should be conservative; sampling everything is expensive (CPU + collector + storage) and wrong for production-bound services.
**Fix**: Default to a small fraction (0.05 or 0.01). Or require an explicit value; treat `<= 0` as "disabled" not "100%".
**Effort**: S
**Phase**: 1

### [HIGH] Tracing always enables Baggage propagator
**File**: `observability/tracing/tracing.go:102-105,114-117`
**Issue**: `propagation.Baggage{}` added unconditionally. Baggage = arbitrary cross-service KV attached to every outgoing request — easy vector for accidental PII propagation, privacy concern if any handler logs it.
**Fix**: Make Baggage opt-in via Config (`WithBaggage bool`). Default to TraceContext only.
**Effort**: S
**Phase**: 1

### [HIGH] auditlog gormstore cursor ignores timestamp tiebreaker
**File**: `observability/auditlog/gormstore/store.go:69,89-91,100-102`
**Issue**: Order is `timestamp DESC, id DESC` but cursor is `WHERE id < ?`. UUIDv7 IDs are roughly time-ordered but not strictly aligned with `timestamp` column (clock skew, batch inserts). Across timestamp boundaries the cursor can skip rows or duplicate them.
**Fix**: Composite cursor `(timestamp, id)` with predicate `WHERE (timestamp, id) < (?, ?)`. Or order purely by `id DESC` (drop timestamp ordering since UUIDv7 carries time).
**Effort**: S
**Phase**: 3

### [HIGH] auditlog gormstore: resource filter doesn't escape LIKE wildcards
**File**: `observability/auditlog/gormstore/store.go:78`
**Issue**: `q.Where("resource LIKE ?", filter.Resource+"%")` doesn't escape `%`, `_`, `\`. Caller-influenced `filter.Resource` (e.g., search query) returns events from other resources by sending `users/%/secrets`. For an audit log this is a security boundary.
**Fix**: Escape the three LIKE metacharacters before appending `%`. Or restrict `Resource` filters to exact match unless an explicit prefix flag is set.
**Effort**: S
**Phase**: 3

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

- [ ] Phase 1: tracing default sample rate 0.05; Baggage opt-in only.
- [ ] Phase 3: auditlog gormstore composite cursor + LIKE escape.
- [ ] Phase 3: `observability/health` ship Liveness/Readiness handlers.
- [ ] Phase 3: `logattr.Secret` + `Email` helpers.
- [ ] Phase 3: tracing `Init` timeout + noop fallback.
- [ ] Phase 3: auditlog memory IPAddress filter; promutil register semantics; SLO label filter.

### Related new packages

- [new/15-observability-pprof-runtime.md](../new/15-observability-pprof-runtime.md) — pprof + runtime metrics on internal port.
- [new/16-observability-red-metrics.md](../new/16-observability-red-metrics.md) — RED middleware with proper buckets.
