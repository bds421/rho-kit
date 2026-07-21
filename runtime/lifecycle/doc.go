// Package lifecycle composes long-running components into a single graceful
// startup / shutdown story.
//
// # Model
//
// A [Component] has two responsibilities:
//
//   - Start(ctx) blocks until ctx is cancelled or the component fails.
//   - Stop(ctx) performs graceful shutdown. The Runner derives per-component
//     contexts from a single shared shutdown budget configured with
//     [WithStopTimeout] (default 30s); each Stop observes ctx.Done() once
//     that shared budget is exhausted, not after a fresh per-component
//     timer.
//
// Component names are supplied externally via [Runner.Add] / [Runner.AddFunc]
// (not a method on Component) so adapters stay free of naming concerns.
//
// [Runner] orchestrates a set of components: Start them concurrently, block
// on OS signals (SIGINT, SIGTERM), then Stop them in reverse registration
// order. If any component's Start exits early with an error, the runner
// cancels the shared context so all peers observe the failure and shut down
// together.
//
// # Adapters
//
// Two adapters are provided out of the box:
//
//   - [NewHTTPServer] wraps a configured *http.Server so its ListenAndServe /
//     Shutdown lifecycle plugs straight into a Runner.
//   - [FuncComponent] / [NewFuncComponent] / [Runner.AddFunc] adapt a single
//     blocking function (typically a worker loop) into a Component.
//
// # Usage outline
//
// A typical service constructs a Runner, registers each top-level component
// (HTTP server, eventbus, cron scheduler, batch workers, leader elector, …),
// and calls Run. Run returns the first start-side error joined with any
// errors observed during shutdown so operators see the full picture.
//
// A second SIGINT during shutdown cancels the in-flight Stop calls so the
// process can exit quickly even when a misbehaving component refuses to
// release its resources.
//
// # Shutdown logs
//
// For each component the Runner emits a "stopping component" log
// immediately before invoking Stop, followed by "component stopped" on
// success or "component stop error" on failure (with the elapsed time
// on the error path). The pre-Stop log is essential when a Stop hangs:
// without it the operator sees only "shutting down components" and
// must wait for the per-component deadline before learning which
// component is unresponsive.
package lifecycle
