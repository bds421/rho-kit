# runtime/ — lifecycle, cron, batchworker, eventbus, concurrency

## Landed

- ✅ **eventbus sync-handler panic recovery** — `callSync` wraps each sync handler with `defer recover()`, converts panic to error, routes into `syncErrs` (commit `6a76329`).
- ✅ **cron.Stop honours its caller ctx** — selects on both stopCtx.Done() and the caller's ctx.Done(); returns ctx.Err() on timeout instead of blocking on cron's own grace period (commit `6a76329`).
- ✅ **lifecycle.Runner signal-goroutine leak** — first selects on ctx.Done() OR runDone; clean exit on normal Run return (commit `6a76329`).
- ✅ **lifecycle.Runner joined start+stop errors** — stopAll error captured separately, joined with startErr via `errors.Join` (commit `6a76329`).
- ✅ **batchworker.Stop cancels in-flight tick** — Start derives a cancellable ctx, stores cancel func; Stop calls it before WaitContext (commit `6a76329`). Also fixes `nextDelay` to return base interval when MaxJitter <= 0.
- ✅ **retry.Loop returns on nil error** — graceful workers no longer restart forever (commit `270c901`).
- ✅ **cron + batchworker histogram buckets widened** — `[0.1, 1, 5, 10, 30, 60, 120, 300, 600, 1800, 3600]` (commit `6a76329`).

## Open

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

### Migration checklist

- [ ] Phase 3: cron per-job timeout; cron ctx synchronization; eventbus Unsubscribe + OnFull policy; FanOut default cap.
