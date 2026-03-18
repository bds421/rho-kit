# Resilience — Retry & Circuit Breaker

Packages: `resilience/retry`, `resilience/circuitbreaker`

## When to Use

| Scenario | Use |
|---|---|
| Transient failures (network blips, 503s) | `retry.Do` with `DefaultPolicy` |
| Long-running worker that must restart on failure | `retry.Loop` with `WorkerPolicy` |
| External service degrading (cascading failure risk) | `resilience/circuitbreaker` |
| External call that may fail and also flap | Both: circuit breaker wraps function, retry wraps CB |
| Permanent errors (bad input, disabled feature) | Neither — use `apperror.NewPermanent()` |

## Retry

### Policies

```go
// Default: 3 retries, 1s base delay, 30s max, 2x factor, ±25% jitter
retry.DefaultPolicy

// Worker: unlimited retries, 3s base, 60s max, resets counter after 30s stable run
retry.WorkerPolicy
```

### Single-Shot Retry

```go
err := retry.Do(ctx, func(ctx context.Context) error {
    return callExternalService(ctx)
}, retry.WithRetryIf(retry.RetryIfNotPermanent))
```

### With Custom Policy

```go
err := retry.DoWith(ctx, retry.DefaultPolicy, func(ctx context.Context) error {
    return callAPI(ctx)
},
    retry.WithMaxRetries(5),
    retry.WithBaseDelay(500*time.Millisecond),
    retry.WithOnRetry(func(err error, attempt int, delay time.Duration) {
        logger.Warn("retrying", "attempt", attempt, "delay", delay, "err", err)
    }),
)
```

### Worker Loop (Infinite Restart)

```go
// Blocks until ctx cancelled. Logs restarts. Resets backoff after 30s of stability.
retry.Loop(ctx, logger, "email-consumer", func(ctx context.Context) error {
    return consumer.Run(ctx) // restarted on error
})
```

### Options

| Option | Description |
|---|---|
| `WithMaxRetries(n)` | 0=no retry, -1=unlimited |
| `WithBaseDelay(d)` | Initial delay |
| `WithMaxDelay(d)` | Delay cap |
| `WithFactor(f)` | Exponential multiplier |
| `WithJitter(f)` | ±fraction (0.25 = ±25%) |
| `WithStableReset(d)` | Reset counter if fn ran this long before failing |
| `WithRetryIf(fn)` | Predicate — return false to stop retrying |
| `WithOnRetry(fn)` | Callback per retry (logging, metrics) |

### RetryIfNotPermanent

```go
retry.WithRetryIf(retry.RetryIfNotPermanent)
```

Skips retry when error wraps `apperror.PermanentError`. Use this for most external calls to avoid retrying structurally broken requests.

## Circuit Breaker

Three states: **Closed** (normal) → **Open** (fail fast) → **Half-Open** (probe).

```go
cb := circuitbreaker.NewCircuitBreaker(5, 30*time.Second,
    circuitbreaker.WithName("payment-gateway"),
    circuitbreaker.WithPermanentSuccess(), // apperror.Permanent = success (not failure)
    circuitbreaker.WithOnStateChange(func(name string, from, to circuitbreaker.State) {
        logger.Warn("circuit breaker state change", "name", name, "from", from, "to", to)
    }),
)

err := cb.Execute(func() error {
    return paymentGateway.Charge(ctx, amount)
})
if errors.Is(err, circuitbreaker.ErrCircuitOpen) {
    return apperror.NewPermanent("payment service temporarily unavailable")
}

cb.State()      // "closed", "open", "half-open"
cb.StateValue() // typed State constant
```

### Options

| Option | Description |
|---|---|
| `WithName(s)` | Label for metrics/logs |
| `WithInterval(d)` | Rolling window for clearing failure counts |
| `WithMaxRequests(n)` | Allowed requests in half-open state |
| `WithOnStateChange(fn)` | Callback on state transitions |
| `WithIsSuccessful(fn)` | Custom success predicate |
| `WithPermanentSuccess()` | Treat `apperror.Permanent` as success |

### Nil Safety

`nil` circuit breaker is safe — `Execute` passes through:
```go
var cb *circuitbreaker.CircuitBreaker // nil
err := cb.Execute(fn) // calls fn directly
```

## Combined Pattern

```go
cb := circuitbreaker.NewCircuitBreaker(5, 30*time.Second)

err := retry.Do(ctx, func(ctx context.Context) error {
    return cb.Execute(func() error {
        return externalService.Call(ctx)
    })
}, retry.WithRetryIf(func(err error) bool {
    // Don't retry when circuit is open — wait for cooldown
    return !errors.Is(err, circuitbreaker.ErrCircuitOpen) &&
        retry.RetryIfNotPermanent(err)
}))
```

## Storage Wrappers

The `storage/retry` and `storage/circuitbreaker` packages provide the same patterns pre-built for storage backends:

```go
retried := storageretry.New(backend, storageretry.WithMaxAttempts(3))
breaker := storagecb.New(retried, storagecb.WithThreshold(5))
```

See [storage.md](storage.md) for composition details.

## Anti-Patterns

- **Never** retry `apperror.PermanentError` — always use `RetryIfNotPermanent` or check manually.
- **Never** retry when circuit is open — it defeats the purpose of fast-failing.
- **Never** use `retry.Loop` for one-shot operations — it never returns until ctx is cancelled.
- **Never** set `MaxRetries: -1` without `StableReset` — backoff grows unbounded without reset.
- **Never** use very short circuit breaker cooldowns (<5s) — half-open probes hit the degraded service too aggressively.
