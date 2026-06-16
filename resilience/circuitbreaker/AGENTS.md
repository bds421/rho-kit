# AGENTS.md — `resilience/circuitbreaker`

## When to use this package

- Wrapping calls to a downstream that may be entirely unavailable for periods (cold backups, paid third-party APIs, batch reporting endpoints).
- "Stop trying for a while, then probe" semantics — exactly the breaker pattern.

## When to use something else

- **Transient failures with eventual recovery in seconds:** `resilience/retry` is leaner — no state machine, no probing.
- **Bulkhead / rate limiting (cap concurrent in-flight requests):** out of kit scope; pair with `httpx/middleware/ratelimit` or a semaphore in your client code.

## Key APIs

- `NewCircuitBreaker(threshold, cooldownPeriod, opts...)` — returns `*CircuitBreaker`. The two required positional args set when the breaker trips (`threshold` consecutive failures) and how long it stays open before probing (`cooldownPeriod time.Duration`). Wraps `github.com/sony/gobreaker/v2` internally.
- `Execute(fn func() error)` — synchronous call. Returns `ErrCircuitOpen` if the breaker is tripped (fast-fail, no call to fn).
- `ExecuteCtx(ctx, fn func(ctx) error)` — same with context propagation; cancelled ctx short-circuits.
- `State()` — string label for observability.
- A **nil receiver is a no-op** — `Execute`/`ExecuteCtx` call fn directly. Composes naturally when breaker wiring is optional.

## Common mistakes

- **Treating `ErrCircuitOpen` as a real error in logs** — the breaker is doing its job. Filter on `errors.Is(err, ErrCircuitOpen)` and log at INFO (or not at all) instead of ERROR.
- **`WithIsSuccessful` that counts `context.Canceled` as a failure** — opens the breaker every time a client cancels (load shedding, page reload). Default exclusion is correct for most services.
- **Composing retry INSIDE a breaker** — every retry attempt counts against the breaker's failure threshold. Put retry OUTSIDE the breaker so retries observe `ErrCircuitOpen` and stop early.
- **Per-tenant breakers** — `WithName` becomes a Prometheus label via the gobreaker callback. Per-tenant breaker names blow up cardinality. Use one breaker per downstream, not per tenant.

## Observability

- OTel: `breaker.Execute` / `breaker.ExecuteCtx` span around the call. `ErrCircuitOpen` is NOT recorded as an error — surfaces as `kit.breaker.tripped=true` attribute.
- No built-in Prometheus metrics; wire via `WithOnStateChange(fn)` to emit your own state-transition counter.
