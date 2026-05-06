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

_All previously open items are now landed — see Landed section above and below._

## Recently Landed (Phase 3)

- ✅ **Cron per-job timeout** — `Scheduler.SetJobTimeout(name, d)` configures a per-run deadline; `wrapJob` derives ctx via `context.WithTimeout` so the job sees `context.DeadlineExceeded`.
- ✅ **Cron ctx synchronization** — `Scheduler` now holds a `sync.RWMutex`; `Start`/`Stop`/`wrapJob` all read/write `ctx`/`cancel`/`jobTimeouts` under the mutex. Passes `go test -race`.
- ✅ **EventBus Unsubscribe** — `Subscribe[E]` returns a `Subscription` token; `Bus.Unsubscribe(sub)` removes the handler safely (atomic slice replacement preserves in-flight Publish snapshots).
- ✅ **EventBus OnFull policy** — `WithOnFull(OnFullDrop|OnFullBlock|OnFullError)` selects the saturation policy; `Block` waits on the publisher ctx, `Error` returns `ErrQueueFull` from `Publish`.
- ✅ **FanOut default cap** — default `maxGoroutines` is `runtime.GOMAXPROCS(0) * 2`. Pass `WithMaxGoroutines(0)` to opt back into unbounded.

### Migration checklist

- [x] Phase 3: cron per-job timeout; cron ctx synchronization; eventbus Unsubscribe + OnFull policy; FanOut default cap.
