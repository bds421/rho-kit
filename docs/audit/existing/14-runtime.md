# runtime/ — lifecycle, cron, batchworker, eventbus, concurrency

### [HIGH] `eventbus`: sync handler panic crashes publisher (no recover)
**File**: `runtime/eventbus/eventbus.go:223-228`
**Issue**: `Publish` invokes sync handlers via `h.fn(ctx, event)` with no `recover()`. Async path uses `runAsync`/`executeTask` with panic recovery; sync handlers don't. One buggy subscriber crashes the publisher's goroutine.
**Fix**: Wrap each sync handler call in `func(){ defer recover(...); h.fn(ctx, event) }()`. Route panic-as-error into `syncErrs`. Optionally call `b.onError`.
**Effort**: S
**Phase**: 1

### [HIGH] Cron `Stop` ignores its context — blocks Runner shutdown indefinitely
**File**: `runtime/cron/cron.go:95-103`
**Issue**: `func (s *Scheduler) Stop(_ context.Context) error` discards parent ctx, then does `<-s.cron.Stop().Done()` — waits for in-flight jobs. A hung job blocks Stop forever. Force-quit (second SIGTERM) cancels `forceCtx` but Stop doesn't observe it.
**Fix**: Accept the ctx; `select` between `<-stopCtx.Done()` and `<-ctx.Done()`. Return `ctx.Err()` on timeout — at least the Runner unblocks.
**Effort**: S
**Phase**: 1

### [HIGH] `lifecycle.Runner` force-signal goroutine leaks on component error
**File**: `runtime/lifecycle/runner.go:99`
**Issue**: The force-quit signal goroutine blocks on `<-ctx.Done()` (signal context) and only then selects on `runDone`. If a component returns an error, `errgroup` cancels `gCtx`, `Run` returns, and `runDone` closes — but the signal goroutine remains blocked because the SIGNAL context was never cancelled. Each failed `Run` call leaks one goroutine. Small for normal long-lived processes; bad for tests, embedded runners, repeated start/stop harnesses.
**Fix**: First `select` on either `ctx.Done()` (signal) or `runDone` (Run finished). Only register the second-signal channel if the signal context actually cancels.
**Effort**: S
**Phase**: 2

### [HIGH] `lifecycle.Runner` drops shutdown errors when a Start error fires first
**File**: `runtime/lifecycle/runner.go:159-165`
**Issue**: `stopAll` runs as another goroutine inside the same `errgroup`. `errgroup.Wait()` returns only the first non-nil error — Start's error. The stop errors are silently dropped — operators never see the cascade of stop failures.
**Fix**: Run `stopAll` outside the errgroup (after `eg.Wait()` returns the start error). Join start error and stop error with `errors.Join`.
**Effort**: S

### [HIGH] `retry.Loop` restarts after a `nil` error
**File**: `resilience/retry/retry.go:147`
**Issue**: `Loop` treats every return from `fn` as a restart condition unless the context is cancelled or `RetryIf` rejects. If `fn` returns nil intentionally (graceful completion), `Loop` logs a restart with a nil error and continues forever. `RetryIf` predicates also receive nil unexpectedly — most predicate implementations don't handle that.
**Fix**: Return immediately on `err == nil`. (If the "always restart" semantic is wanted by some caller, document and rename it as `LoopForever` and add a separate error-only restart variant.)
**Effort**: S
**Phase**: 1

### [HIGH] BatchWorker `Stop` cannot stop a worker without cancelling parent ctx
**File**: `runtime/batchworker/batchworker.go:121-152`
**Issue**: `Start` blocks on parent ctx. `Stop` only waits on `w.done` (closed inside Start). No internal cancel func. If `Stop` is called without first cancelling the ctx passed to `Start`, Stop blocks until its own ctx expires while Start is still running. Bundled lifecycle Runner masks this; standalone usage hangs.
**Fix**: `Start` derives a cancellable ctx, stores cancel func; `Stop` calls it. Mirror `FuncComponent` pattern.
**Effort**: S

### [HIGH] BatchWorker `nextDelay` panics on small intervals
**File**: `runtime/batchworker/batchworker.go:189-196`
**Issue**: `maxJitter := time.Duration(float64(w.interval) * w.jitter)` truncates to 0 when `interval*jitter < 1ns`. `rand.Int64N(0)` panics. The defensive panic recovery in `runBatch` doesn't cover the loop in `Start`.
**Fix**: Guard with `if maxJitter <= 0 { return w.interval }`. Or enforce a minimum interval in `New`.
**Effort**: S

### [MEDIUM] Cron job context shared across runs without per-run cancellation
**File**: `runtime/cron/cron.go:106-112`
**Issue**: All job invocations receive `s.ctx` (lifetime-of-scheduler). Long-running jobs have no per-run timeout/deadline; they run until done or scheduler stops. No `WithJobTimeout` option.
**Fix**: Add per-job timeout option; wrap ctx with `context.WithTimeout` inside `wrapJob`.

### [MEDIUM] Cron `s.ctx` read without synchronization
**File**: `runtime/cron/cron.go:86-89,108`
**Issue**: `Start` writes `s.ctx, s.cancel`; `Stop` reads `s.cancel`; `wrapJob` reads `s.ctx`. None mutex-protected. Happy path works (Start completes before any tick fires) but `go test -race` will flag concurrent Start/Stop/wrapJob reads under load.
**Fix**: Guard with mutex, or use `atomic.Pointer[context.Context]`.

### [MEDIUM] EventBus: handlers cannot be unsubscribed
**File**: `runtime/eventbus/eventbus.go` (no `Unsubscribe`)
**Issue**: `Subscribe` only appends. Tests, dynamic plugins, or modules re-registering on reload leak handlers and double-fire.
**Fix**: Add `Unsubscribe(token)` returning a token from Subscribe; ensure removal is safe under concurrent Publish via the existing snapshot pattern.

### [MEDIUM] EventBus async dispatch silently drops events when queue full
**File**: `runtime/eventbus/pool.go:96-113`
**Issue**: Full queue → task dropped + `events_dropped_total++`. No backpressure option.
**Fix**: Add per-bus or per-handler `OnFull` policy: `Drop` (current), `Block` (with ctx), `Error` (return to publisher).

### [LOW] FanOut default is unbounded goroutines
**File**: `runtime/concurrency/fanout.go:91-96`
**Issue**: Without `WithMaxGoroutines`, FanOut launches one goroutine per task. Doc warns but the default is permissive.
**Fix**: Default `maxGoroutines` to `runtime.GOMAXPROCS(0) * 2`. Require `WithMaxGoroutines(0)` to opt out.

### [LOW] Histogram buckets use Prometheus defaults — too narrow for batch
**File**: `runtime/batchworker/metrics.go:28` + `runtime/cron/metrics.go:28`
**Issue**: `prometheus.DefBuckets` tops out at 10s. Cron jobs and batch workers commonly run for minutes; everything beyond 10s lands in `+Inf`.
**Fix**: Wider buckets `[]float64{0.1, 1, 5, 10, 30, 60, 120, 300, 600, 1800, 3600}`. Or add `WithBuckets` option.

### Migration checklist

- [ ] Phase 1: eventbus sync-handler recover; cron Stop ctx; batchworker Stop cancel + nextDelay guard.
- [ ] Phase 1: lifecycle.Runner joined start+stop errors.
- [ ] Phase 1: cron + batchworker histogram buckets.
- [ ] Phase 1: `retry.Loop` returns on nil error (no infinite restart of "graceful complete" workers).
- [ ] Phase 2: lifecycle.Runner signal-goroutine leak fix (select on runDone OR ctx.Done).
- [ ] Phase 3: cron per-job timeout; cron ctx synchronization; eventbus Unsubscribe + OnFull policy; FanOut default cap.
