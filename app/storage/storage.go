// Package storage is the lazy app-module wrapper for the kit's
// object-storage [github.com/bds421/rho-kit/infra/v2/storage]
// interfaces. Services pass [storage.Module] to [app.Builder.With]
// to register a default backend and / or a manager populated with
// named backends.
//
//	app.New(name, ver, cfg).
//	    With(storage.Module(s3Backend,
//	        storage.WithNamed("uploads", uploadsBackend),
//	        storage.WithNamed("archive", archiveBackend),
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
//
// # Backend ownership
//
// All backends passed to [Module] (the default backend and any
// [WithNamed] backend) are caller-owned. The module never closes them on
// shutdown — [storageModule.Stop] is a no-op — so callers retain
// responsibility for closing buffered/pooled backends (e.g. SFTP or
// encryption decorators). This differs from the postgres/nats/redis
// bridges, which own and close the handles they construct internally.
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

// WithNamed adds a named backend to the storage manager. Multiple
// WithNamed options stack; the first registered backend is the
// default in the manager unless [storage.Manager.SetDefault] is
// called inside the router.
//
// Panics if name is empty or backend is nil.
func WithNamed(name string, backend storage.Storage) Option {
	if name == "" {
		panic("app/storage: WithNamed requires a non-empty name")
	}
	if backend == nil {
		panic("app/storage: WithNamed requires a non-nil backend")
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
	// Defensive clone — caller-held option closures append to
	// cfg.named / cfg.checks via the variadic; once Module returns
	// the module owns the slices and callers must not mutate them
	// through aliasing.
	cfg.named = append([]namedSpec(nil), cfg.named...)
	cfg.checks = append([]health.DependencyCheck(nil), cfg.checks...)
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

// Stop is a no-op: backend lifecycle stays with the caller. Unlike the
// postgres/nats/redis bridges (which own the handle they construct), the
// default backend and every [WithNamed] backend here are caller-supplied
// — the caller may share a single backend across modules or close it on
// its own schedule, so this module must not close them. The internal
// *storage.Manager built in [storageModule.Init] only indexes those
// caller-owned backends; closing it would close resources this module
// does not own, so it is deliberately left to the caller too. Callers
// that want reverse-order [storage.Manager.Close] semantics on shutdown
// can register a closer for the value returned by [Manager] themselves.
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
