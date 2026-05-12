package app

import (
	"context"
	"fmt"
	"log/slog"
	"runtime/debug"
	"time"

	"github.com/bds421/rho-kit/core/v2/redact"
	"github.com/bds421/rho-kit/infra/v2/storage"
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

type storageSpec struct {
	name    string
	backend storage.Storage
}

type keyedLimiterSpec struct {
	name     string
	requests int
	window   time.Duration
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

func validateDependencyChecks(checks []health.DependencyCheck, where string) {
	for i, check := range checks {
		validateDependencyCheck(check, fmt.Sprintf("%s[%d]", where, i))
	}
}

// buildIntegrationModules converts builder config from the With*() methods
// (WithPostgres, WithRedis, WithRabbitMQ, WithTracing, WithJWT) into
// internal modules. The With*() methods are the primary public API;
// modules are the internal implementation. These modules are prepended
// to user-registered modules so built-in infrastructure initializes first.
//
// Registration order matters: tracing -> httpclient -> jwt, because each
// module depends on the previous one during Init.
func (b *Builder) buildIntegrationModules() []Module {
	var modules []Module

	// Tracing must come first -- httpClientModule reads its Active() state.
	if b.tracingCfg != nil {
		modules = append(modules, newTracingModule(*b.tracingCfg))
	}

	// HTTP client is always created -- other modules and infra need it.
	modules = append(modules, newHTTPClientModule(b.tracingCfg != nil))

	if b.jwksURL != "" {
		modules = append(modules, newJWTModule(jwtModuleConfig{
			jwksURL:        b.jwksURL,
			expectedIssuer: b.jwtIssuer,
			allowAnyIssuer: b.jwtAllowAnyIssuer,
			audience:       b.jwtAudience,
		}))
	}

	if b.pasetoProvider != nil {
		modules = append(modules, newPasetoModule(b.pasetoProvider))
	}

	if b.pgxCfg != nil {
		modules = append(modules, newPgxModule(*b.pgxCfg, b.migrationsDir))
	}

	if b.redisOpts != nil {
		modules = append(modules, newRedisModule(b.redisOpts, b.allowPlaintextRedis, b.redisConnOpts...))
	}

	if b.mqURL != "" {
		m := newMessagingModule(b.mqURL)
		m.criticalBroker = b.criticalBroker
		m.messageSizeLimiter = b.messageSizeLimiter
		modules = append(modules, m)
	}

	if b.natsCfg != nil {
		m := newNatsModule(*b.natsCfg)
		m.messageSizeLimiter = b.messageSizeLimiter
		modules = append(modules, m)
	}

	if b.leaderElector != nil {
		modules = append(modules, newLeaderModule(b.leaderElector))
	}

	return modules
}

// buildStorageManager creates a Manager from the named storage specs.
// Returns nil if no specs were registered.
func buildStorageManager(specs []storageSpec) *storage.Manager {
	if len(specs) == 0 {
		return nil
	}
	mgr := storage.NewManager()
	for _, s := range specs {
		mgr.Register(s.name, s.backend)
	}
	return mgr
}
