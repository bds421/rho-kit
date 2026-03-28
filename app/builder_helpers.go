package app

import (
	"context"
	"time"

	"github.com/bds421/rho-kit/infra/storage"
)

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
// (WithMySQL, WithPostgres, WithRedis, WithRabbitMQ, WithTracing, WithJWT,
// WithGRPC) into internal modules. The With*() methods are the primary public
// API; modules are the internal implementation. These modules are prepended to
// user-registered modules so built-in infrastructure initializes first.
//
// Registration order matters: tracing -> httpclient -> jwt, because each module
// depends on the previous one during Init.
//
// The returned *databaseModule is non-nil when a database is configured. Run()
// uses it to check for seed early-exit after module initialization.
// The returned *grpcModule is non-nil when gRPC is configured. Run() uses it
// to start the gRPC server and register the health service.
func (b *Builder) buildIntegrationModules() ([]Module, *databaseModule, *grpcModule) {
	var modules []Module
	var dbMod *databaseModule
	var grpcMod *grpcModule

	// Tracing must come first -- httpClientModule reads its Active() state.
	if b.tracingCfg != nil {
		modules = append(modules, newTracingModule(*b.tracingCfg))
	}

	// HTTP client is always created -- other modules and infra need it.
	// It reads tracing state when a tracing module is registered.
	modules = append(modules, newHTTPClientModule(b.tracingCfg != nil))

	// JWT depends on httpClientModule for the HTTP client.
	if b.jwksURL != "" {
		modules = append(modules, newJWTModule(b.jwksURL))
	}

	if b.dbMySQLCfg != nil || b.dbPgCfg != nil {
		dbMod = newDatabaseModule(databaseModuleConfig{
			mysqlCfg:      b.dbMySQLCfg,
			pgCfg:         b.dbPgCfg,
			poolCfg:       *b.dbPoolCfg,
			namespace:     b.dbNamespace,
			migrationsDir: b.migrationsDir,
			seedFn:        b.seedFn,
			metrics:       b.dbMetrics,
		})
		modules = append(modules, dbMod)
	}

	if b.redisOpts != nil {
		modules = append(modules, newRedisModule(b.redisOpts, b.redisConnOpts...))
	}

	if b.mqURL != "" {
		m := newMessagingModule(b.mqURL)
		m.criticalBroker = b.criticalBroker
		modules = append(modules, m)
	}

	if b.grpcRegistrar != nil {
		grpcMod = newGRPCModule(b.grpcRegistrar, b.grpcAddr, b.grpcOpts)
		modules = append(modules, grpcMod)
	}

	return modules, dbMod, grpcMod
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
