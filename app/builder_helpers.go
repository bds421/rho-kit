package app

import (
	"context"
	"log/slog"
	"time"

	"github.com/bds421/rho-kit/infra/storage"
)

// runShutdownHooks invokes every registered hook synchronously, with
// individual panic recovery and a per-hook timeout. Each hook gets a
// fresh 10s context so a misbehaving hook cannot block the rest of the
// shutdown sequence.
//
// Called from the lifecycle.Runner's BeforeStop callback so hooks see
// live infrastructure connections — DB pools, Redis clients, message
// brokers are still open at this point. Component teardown runs only
// after this function returns.
func runShutdownHooks(_ context.Context, hooks []func(context.Context), logger *slog.Logger) {
	for i, fn := range hooks {
		func(idx int, hook func(context.Context)) {
			defer func() {
				if rec := recover(); rec != nil {
					logger.Error("shutdown hook panicked",
						"hook_index", idx,
						"panic", rec,
					)
				}
			}()
			hookCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			done := make(chan struct{})
			go func() {
				defer close(done)
				hook(hookCtx)
			}()
			select {
			case <-done:
			case <-hookCtx.Done():
				logger.Error("shutdown hook timed out", "hook_index", idx)
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
		modules = append(modules, newRedisModule(b.redisOpts, b.redisConnOpts...))
	}

	if b.mqURL != "" {
		m := newMessagingModule(b.mqURL)
		m.criticalBroker = b.criticalBroker
		modules = append(modules, m)
	}

	if b.natsCfg != nil {
		modules = append(modules, newNatsModule(*b.natsCfg))
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
