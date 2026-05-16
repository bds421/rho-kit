# AGENTS.md — `resilience/retry`

## When to use this package

- Wrapping a transient-failure-prone call (Redis, HTTP, database) with retry + exponential backoff.
- Implementing a worker loop with restart-on-error (`Loop`).
- Per-call (`Do`/`DoWith`) for one-shot operations that can fail and recover.

## When to use something else

- **Broker-side retry (AMQP DLX, NATS max-deliver, Redis Streams pending-entry-list):** prefer setting `BindingSpec.Retry` and letting the broker drive redelivery instead of looping in-handler.
- **You need a circuit breaker semantic ("stop trying after N failures"):** `resilience/circuitbreaker` instead. Combining both is reasonable for high-failure-rate downstreams.
- **Worker that should restart forever:** `retry.Loop` is the right answer. Don't build your own `for { fn(); time.Sleep(...) }` — `Loop` handles stable-cycle resets, panic recovery, ctx cancellation, and structured logging.

## Key APIs

- `Do(ctx, fn, opts...)` — uses `DefaultPolicy()` (3 retries, 500ms base, 30s cap, exponential).
- `DoWith(ctx, base, fn, opts...)` — start from an explicit policy.
- `Loop(ctx, logger, component, fn, opts...)` — restart-on-error worker, uses `WorkerPolicy()` defaults (unlimited retries, longer backoff, stable-cycle reset).
- `WithMaxRetries(n)` — 0 means "no retries (run once)", negative means "unlimited".
- `WithBaseDelay`, `WithMaxDelay`, `WithFactor`, `WithJitter`, `WithMaxElapsedTime`, `WithStableReset`, `WithDelayOverride`, `WithRetryIf`, `WithOnRetry`.

## Common mistakes

- **`WithMaxRetries(-1)` for a unary call** — unlimited retries on a synchronous call is almost always wrong. Either bound it with `WithMaxRetries`, `WithMaxElapsedTime`, or both.
- **Retrying non-transient errors** — by default the kit retries everything. Use `WithRetryIf(fn)` to gate retries on `errors.Is(err, sentinel)`.
- **`Loop` with handler that ALSO retries inside** — double-counted attempts, slow recovery. Pick one layer.
- **Retrying a context-cancelled call** — wasteful. The default `RetryIf` excludes `context.Canceled` and `DeadlineExceeded` indirectly via `fn`'s ctx check, but custom predicates should preserve this.

## Observability

- OTel: `retry.Do` / `retry.DoWith` span around the entire retry loop. Per-attempt detail is folded into the parent span (no per-attempt child spans — would inflate exporter load on tight loops). Attribute: `kit.retry.max_retries`.
- No Prometheus metrics by default; instrument via `WithOnRetry(fn)` callback if attempt count matters.
