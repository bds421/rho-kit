// Package storage is the lazy app-module wrapper for the kit's
// object-storage [github.com/bds421/rho-kit/infra/v2/storage]
// interfaces. Services pass [storage.Module] to [app.Builder.With]
// to register a default backend and / or a manager populated with
// named backends.
//
//	app.New(name, ver, cfg).
//	    With(storage.Module(s3Backend,
//	        storage.Named("uploads", uploadsBackend),
//	        storage.Named("archive", archiveBackend),
//	        storage.WithHealthCheck(s3HealthCheck),
//	    )).
//	    Router(routerFn).
//	    Run()
//
// Handlers reach for the default backend via [storage.Backend] and
// the named-backend manager via [storage.Manager]:
//
//	primary := storage.Backend(infra)            // *infra/storage.Storage
//	uploads := storage.Manager(infra).Backend("uploads")
//
// Keeping object-storage backends in this bridge module keeps the
// AWS / GCP / Azure / SFTP SDK closures out of services that do not
// need them (FR-067).
package storage

import (
	"context"

	"github.com/bds421/rho-kit/app/v2"
	"github.com/bds421/rho-kit/infra/v2/storage"
	"github.com/bds421/rho-kit/observability/v2/health"
)

// ModuleName is the registered Module.Name() value.
const ModuleName = "storage"

// Resource keys.
const (
	// ResourceBackendKey is the [app.Infrastructure.Resource] key
	// under which [Module] publishes the default backend.
	ResourceBackendKey = "github.com/bds421/rho-kit/app/storage.backend"
	// ResourceManagerKey is the [app.Infrastructure.Resource] key
	// under which [Module] publishes the *storage.Manager populated
	// with named backends. Absent when no [Named] option is passed.
	ResourceManagerKey = "github.com/bds421/rho-kit/app/storage.manager"
)

// Option configures [Module].
type Option func(*config)

type config struct {
	named  []namedSpec
	checks []health.DependencyCheck
}

type namedSpec struct {
	name    string
	backend storage.Storage
}

// Named adds a named backend to the storage manager. Multiple Named
// options stack; the first registered backend is the default in the
// manager unless [storage.Manager.SetDefault] is called inside the
// router.
//
// Panics if name is empty or backend is nil.
func Named(name string, backend storage.Storage) Option {
	if name == "" {
		panic("app/storage: Named requires a non-empty name")
	}
	if backend == nil {
		panic("app/storage: Named requires a non-nil backend")
	}
	return func(c *config) {
		c.named = append(c.named, namedSpec{name: name, backend: backend})
	}
}

// WithHealthCheck adds a dependency check to the kit's readiness
// probe. Multiple WithHealthCheck options stack.
//
// Panics if check fields are invalid (empty name or nil func).
func WithHealthCheck(check health.DependencyCheck) Option {
	if check.Name == "" {
		panic("app/storage: WithHealthCheck requires a non-empty Name")
	}
	if check.Check == nil {
		panic("app/storage: WithHealthCheck requires a non-nil Check")
	}
	return func(c *config) { c.checks = append(c.checks, check) }
}

// Module returns an [app.Module] that registers backend as the
// default object-storage backend and, if [Named] options are
// supplied, builds a *storage.Manager populated with the named
// entries.
//
// Panics if backend is nil — startup-time misconfiguration must
// fail fast.
func Module(backend storage.Storage, opts ...Option) app.Module {
	if backend == nil {
		panic("app/storage: Module requires a non-nil backend")
	}
	cfg := config{}
	for _, opt := range opts {
		if opt == nil {
			panic("app/storage: Module option must not be nil")
		}
		opt(&cfg)
	}
	return &storageModule{backend: backend, cfg: cfg}
}

type storageModule struct {
	backend storage.Storage
	cfg     config
	manager *storage.Manager
}

func (m *storageModule) Name() string { return ModuleName }

func (m *storageModule) Init(_ context.Context, _ app.ModuleContext) error {
	if len(m.cfg.named) == 0 {
		return nil
	}
	m.manager = storage.NewManager()
	for _, s := range m.cfg.named {
		m.manager.Register(s.name, s.backend)
	}
	return nil
}

func (m *storageModule) Populate(infra *app.Infrastructure) {
	infra.SetResource(ResourceBackendKey, m.backend)
	if m.manager != nil {
		infra.SetResource(ResourceManagerKey, m.manager)
	}
}

func (m *storageModule) Stop(_ context.Context) error { return nil }

func (m *storageModule) HealthChecks() []health.DependencyCheck {
	return m.cfg.checks
}

// Backend returns the default backend registered via [Module], or
// nil if [Module] was not registered with the Builder.
func Backend(infra app.Infrastructure) storage.Storage {
	v, ok := infra.Resource(ResourceBackendKey)
	if !ok {
		return nil
	}
	s, _ := v.(storage.Storage)
	return s
}

// Manager returns the populated *storage.Manager, or nil if no
// [Named] options were passed to [Module].
func Manager(infra app.Infrastructure) *storage.Manager {
	v, ok := infra.Resource(ResourceManagerKey)
	if !ok {
		return nil
	}
	m, _ := v.(*storage.Manager)
	return m
}
