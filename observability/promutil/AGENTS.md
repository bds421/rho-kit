# AGENTS.md — `observability/promutil`

## When to use this package

- Constructing ANY Prometheus collector that has a label position where caller-controlled values might land.
- Registering a collector via `MustRegisterOrGet` (handles "already registered" gracefully — duplicate registration is the #1 way to crash tests).

## When to use something else

- **Building dashboards / alerts:** not in scope — see `observability/dashboards/`.
- **Custom metric formats (StatsD, OTel exporter direct):** not in scope.

## Key APIs

- `OpaqueLabelValue(name, value)` — projects an arbitrary caller-controlled value through a bounded hash + length cap so it cannot inflate Prometheus cardinality beyond a small bucket count.
- `ValidateStaticLabelValue(name, value)` — returns nil iff `value` is a static-label-safe string (no control bytes, length cap, character set). Call at CONSTRUCTION time so misconfiguration surfaces at startup.
- `MustRegisterOrGet(reg, collector)` — registers, OR returns the already-registered collector if a peer wave registered it first. Fixes the "two NewMetrics calls in tests, second fails" footgun.
- `labelguard.Guard` — drops + counts disallowed label values when a collector is being used WRONG by a downstream service. Wave 173's `kit-doctor` extensions detect missing labelguard wiring.

## Common mistakes

- **Putting tenant ID / user ID / request ID directly into a label** — explodes cardinality. Either use `OpaqueLabelValue` or omit the dimension.
- **`prometheus.NewCounterVec` + `reg.MustRegister(...)` directly** — duplicate-registration panics on the second test. Always use `MustRegisterOrGet`.
- **Calling `ValidateStaticLabelValue` at emit time instead of construction time** — late detection. The kit's pattern is to validate at `NewMetrics` so misconfiguration is caught at process startup.
- **Treating `OpaqueLabelValue` as a hashing function** — it's specifically tuned for Prometheus label hygiene: bounded character set, length cap, deterministic. Don't use it for general-purpose hashing.

## Cardinality discipline

This package exists because Prometheus cardinality is the most common production-metric mistake. **Every kit collector with a caller-controlled label position uses one of:**
- `OpaqueLabelValue` (high-entropy values, e.g. close codes, channel names)
- `ValidateStaticLabelValue` at construction (low-entropy, operator-set values, e.g. Lease namespace+name)
- A bounded enum (fixed set of strings: `outcome={success,failure,...}`)

When in doubt, OpaqueLabelValue.
