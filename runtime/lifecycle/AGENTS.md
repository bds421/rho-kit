# AGENTS.md — `runtime/lifecycle`

## When to use this package

- Service composes multiple long-running components (HTTP server, gRPC server, message consumers, scheduled jobs, leader-election runners) that must start and stop together.
- Want signal-based graceful shutdown with bounded stop budgets per component.
- Want panic-in-component to be a structured error returned from `Run`, not a process crash.

## When to use something else

- **Single long-running goroutine, no signal handling needed:** plain `go fn(ctx); <-ctx.Done()` is fine.
- **You're inside `Run` and want to fire a quick task during shutdown:** use `WithBeforeStop(fn)` instead of squeezing it into a component.

## Key APIs

- `NewRunner(logger, opts...)` — central registry. Add components, then call `Run(ctx)`.
- `(*Runner).Add(name, component Component)` — registers a `Component` (`Start(ctx) error` + `Stop(ctx) error`).
- `(*Runner).AddFunc(name, fn)` — convenience for a function-shaped component (wraps as `FuncComponent`).
- `(*Runner).Run(ctx)` — blocks until SIGINT/SIGTERM or any component exits. Stops everything in reverse registration order with per-component budgets.
- `WithStopTimeout(d)` — global stop budget. Per-component budget is `max(1s, min(stopTimeout/N, 5s))`.
- `WithBeforeStop(fn)` — runs synchronously before component teardown. DB / broker connections are still live.
- `HTTPServer(srv)` — adapts `*http.Server` to `Component`. Panics at construction if `ReadHeaderTimeout=0` or `Handler=nil`.
- `NewFuncComponent(fn)` — wraps a function. One-shot; cannot be restarted.

## Common mistakes

- **Long-running `Start` that ignores ctx** — `Stop` will time out because the component never returns. Always `select` on `<-ctx.Done()` in long-lived loops.
- **Calling `Run` twice on the same `Runner`** — single-cycle by design. Construct a new `Runner` if you need to restart everything.
- **`AddFunc` for a component that needs explicit `Stop` semantics** — `FuncComponent` cancels ctx and waits for the function to return. If your function needs an OUT-OF-BAND signal (e.g. flushing a buffer that's not ctx-aware), implement `Component` directly.
- **`HTTPServer` without setting `ReadHeaderTimeout`** — the constructor panics. The kit refuses to let you ship a slow-loris-vulnerable server.
- **Second SIGINT during shutdown** — the runner force-cancels stop timeouts (graceful → immediate). Operators expect this; don't intercept SIGINT yourself.

## Observability

- OTel: `lifecycle.Start` / `lifecycle.Stop` spans (one per component lifecycle phase) carrying `kit.component.name`. `http.ErrServerClosed` is NOT recorded as an error (clean-shutdown signal).
- Structured logs: "starting components", "component starting", "shutting down components", "stopping component", "component stopped" — every entry carries `component` attribute.
