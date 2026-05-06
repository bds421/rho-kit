# observability/ — auditlog, health, logattr, logging, promutil, slo, tracing

## Landed

- ✅ **Tracing default sample rate dropped to 0.05** — was 1.0, which is wrong-shape for a kit-level default; collector + storage cost goes from impossible to budgeted (commit `1198dd5`).
- ✅ **Tracing Baggage opt-in only** — `Config.EnableBaggage` gates the `propagation.Baggage{}` propagator; default is TraceContext only, eliminating accidental PII propagation across services (commit `1198dd5`).
- ✅ **auditlog gormstore composite cursor** — pagination predicate is now `(timestamp, id) DESC` so events with identical microseconds don't get skipped or duplicated across page boundaries (commit `1198dd5`).
- ✅ **auditlog gormstore LIKE escape** — Resource filter uses `LIKE ? ESCAPE '\'` with `%` / `_` / `\` escaped in caller input (commit `1198dd5`).
- ✅ **auditlog gormstore signed cursors** — base64url(payload).base64url(HMAC); `decodeCursor` returns `ErrCursorInvalid` on tamper or cross-secret cursor; `WithCursorSecret` lets multi-replica deployments share signing key (commit `98f05e4`).

## Open

_Closed — see Recently Landed below._

## Recently Landed (Phase 3)

- ✅ **health.Liveness / health.Readiness handlers** — `Liveness(version)` always returns 200; `Readiness(*Checker)` returns 503 on `StatusUnhealthy` and 200 on Healthy/Degraded (degraded keeps the pod in rotation).
- ✅ **logattr.Secret / logattr.Email** — `Secret(key, val)` emits `<redacted N bytes sha256:abc12345>` (length-preserving + correlatable); `Email(addr)` masks the local part while keeping the domain visible.
- ✅ **Tracing init bound + fallback** — `Config.InitTimeout` (default 5s) wraps the OTLP exporter handshake. On dial failure Init falls back to a noop provider; `Config.OnInitFallback` reports the error. `InitTimeout < 0` disables the bound for callers that prefer the parent ctx.
- ✅ **auditlog MemoryStore IPAddress filter** — `matchesFilter` now honours `Filter.IPAddress`, matching the SQL store.
- ✅ **promutil.Register** — new `(prometheus.Collector, error)` API exposes the "already-registered, reusing existing" path; `RegisterCollector` keeps panic-on-conflict but now logs at debug on the AlreadyRegistered branch via `Register` internally.
- ✅ **SLO LatencyLabelFilter** — `SLO.LatencyLabelFilter` (LabelFilter) restricts which histogram label combinations contribute to the percentile, so a per-route p99 SLO no longer mixes routes.

### Migration checklist

- [x] Phase 3: `observability/health` ship Liveness/Readiness handlers.
- [x] Phase 3: `logattr.Secret` + `Email` helpers.
- [x] Phase 3: tracing `Init` timeout + noop fallback.
- [x] Phase 3: auditlog memory IPAddress filter; promutil register semantics; SLO label filter.

### Related new packages

- [new/15-observability-pprof-runtime.md](../new/15-observability-pprof-runtime.md) — pprof + runtime metrics on internal port.
- [new/16-observability-red-metrics.md](../new/16-observability-red-metrics.md) — RED middleware with proper buckets.
