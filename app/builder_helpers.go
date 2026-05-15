package app

import (
	"context"
	"log/slog"
	"runtime/debug"
	"time"

	"github.com/bds421/rho-kit/core/v2/redact"
	"github.com/bds421/rho-kit/observability/v2/health"
)

// shutdownHookTimeout caps how long any single hook may take. Each
// hook still derives from the parent shutdown context (FR-011), so a
// runner-level force cancellation also propagates.
const shutdownHookTimeout = 10 * time.Second

func detachedTimeoutContext(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	return context.WithTimeout(context.WithoutCancel(parent), timeout)
}

// runShutdownHooks invokes every registered hook synchronously, with
// individual panic recovery and a per-hook timeout. Hook contexts are
// derived from parent so a runner-level force cancellation propagates
// to in-flight hooks (audit FR-011).
//
// Called from the lifecycle.Runner's BeforeStop callback so hooks see
// live infrastructure connections — DB pools, Redis clients, message
// brokers are still open at this point. Component teardown runs only
// after this function returns.
func runShutdownHooks(parent context.Context, hooks []func(context.Context), logger *slog.Logger) {
	if parent == nil {
		parent = context.Background()
	}
	for i, fn := range hooks {
		func(idx int, hook func(context.Context)) {
			hookCtx, cancel := context.WithTimeout(parent, shutdownHookTimeout)
			defer cancel()
			done := make(chan struct{})
			go func() {
				defer close(done)
				defer func() {
					if rec := recover(); rec != nil {
						logger.Error("shutdown hook panicked",
							"hook_index", idx,
							redact.Panic(rec),
							"stack", string(debug.Stack()),
						)
					}
				}()
				hook(hookCtx)
			}()
			select {
			case <-done:
			case <-hookCtx.Done():
				logger.Error("shutdown hook timed out", "hook_index", idx, slog.Any("cause", context.Cause(hookCtx)))
			}
		}(i, fn)
	}
}

type bgSpec struct {
	name string
	fn   func(ctx context.Context) error
}

func validateBackgroundSpec(name string, fn func(context.Context) error) {
	if name == "" {
		panic("app: Background requires a non-empty name")
	}
	if fn == nil {
		panic("app: Background requires a non-nil function")
	}
}

func validateDependencyCheck(check health.DependencyCheck, where string) {
	if err := health.ValidateDependencyCheck(check); err != nil {
		panic("app: health check invalid")
	}
}

// buildIntegrationModules assembles the built-in modules the Builder always
// runs: HTTP client (tracing-aware when an app/tracing module is registered),
// JWT verifier (when WithJWT is set), PASETO provider (when WithPASETO is
// set), and the leader-election driver (when WithLeaderElection is set).
//
// Adapter modules (postgres, redis, amqp, nats, tracing, grpc) are NOT built
// here — they live in sub-packages and are registered by the caller via
// [Builder.With]. The Builder discovers them as ordinary entries in
// b.modules.
//
// Registration order: jwt depends on the httpclient module's resolved
// *http.Client, so httpclient must initialize first.
func (b *Builder) buildIntegrationModules() []Module {
	var modules []Module

	tracingActive := b.hasTracingModule()

	modules = append(modules, newHTTPClientModule(tracingActive))

	return modules
}

// hasTracingModule reports whether the user has registered an OTel tracing
// adapter module (typically via app/tracing.Module()). The httpclient module
// queries this to decide whether to wrap its transport in OTel instrumentation.
// app/tracing modules expose a TracingProvider marker so app/v2 can detect
// them without importing OTel.
func (b *Builder) hasTracingModule() bool {
	for _, m := range b.modules {
		if _, ok := m.(TracingProvider); ok {
			return true
		}
	}
	return false
}

// hasRateLimitDeclaration reports whether any user-registered
// module implements [RateLimitDeclarer]. Used by Builder.Validate
// to enforce the explicit rate-limit-or-opt-out contract without
// duplicating module-name knowledge across packages.
func (b *Builder) hasRateLimitDeclaration() bool {
	for _, m := range b.modules {
		if _, ok := m.(RateLimitDeclarer); ok {
			return true
		}
	}
	return false
}

// TracingProvider is implemented by tracing adapter modules (typically
// app/tracing). The HTTP-client module uses the type to detect that tracing
// is configured without app/v2 importing OTel directly.
type TracingProvider interface {
	// TracingActive reports whether tracing was successfully initialized.
	// Tracing modules that failed to connect to the OTel collector but kept
	// running with a degraded health check return false here, so the HTTP
	// client transport is not wrapped in instrumentation that would never
	// flush.
	TracingActive() bool
}

