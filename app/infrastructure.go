package app

import (
	"context"
	"crypto/tls"
	"log/slog"
	"net/http"
	"sync"
	"time"

	kitauthz "github.com/bds421/rho-kit/authz/v2"
	"github.com/bds421/rho-kit/data/v2/actionlog"
	"github.com/bds421/rho-kit/data/v2/approval"
	"github.com/bds421/rho-kit/data/v2/budget"
	"github.com/bds421/rho-kit/httpx/v2"
	"github.com/bds421/rho-kit/infra/v2/storage"
	"github.com/bds421/rho-kit/observability/v2/auditlog"
	"github.com/bds421/rho-kit/observability/v2/health"
	"github.com/bds421/rho-kit/runtime/v2/eventbus"
	"github.com/bds421/rho-kit/security/v2/netutil"
)

// RouterFunc builds the service's HTTP handler from the initialized
// infrastructure. It is called after all With*() infrastructure is set
// up but before the server starts.
type RouterFunc func(infra Infrastructure) http.Handler

// Infrastructure is the collection of initialized infrastructure components
// passed to the RouterFunc. Nil fields indicate the corresponding With*()
// method (or sub-package Module) was not registered.
//
// v2.0.0 lazy-adapter refactor: adapter-typed fields (Postgres pool, Redis
// client, AMQP/NATS connections, gRPC server) moved out of this struct into
// per-adapter sub-packages (app/postgres, app/redis, app/amqp, app/nats,
// app/grpc). Sub-packages publish their resources via [Infrastructure.Resources]
// and expose typed `Get(infra)` helpers so app/v2 no longer transitively pulls
// pgx, go-redis, amqp091, nats.go, otelgrpc, or grpc-go.
//
// The callback fields (Background, SetCustomReadiness, AddHealthCheck) are
// only valid during the synchronous execution of RouterFunc. Calling them
// after RouterFunc returns will panic (lateBgsFrozen guard). This is by
// design: goroutines registered after the Builder has started the lifecycle
// would be silently lost. If you need late-bound goroutines, start them
// inside the function passed to Background — that function runs under the
// lifecycle Runner's supervision.
type Infrastructure struct {
	Logger    *slog.Logger
	ClientTLS *tls.Config
	ServerTLS *tls.Config

	// TLSCertSource is the hot-rotation source supplied by
	// [Builder.ReloadingTLS]. Nil when reloading TLS is not
	// configured. Services that build their own *tls.Config — broker
	// adapters, gRPC dial loops, custom HTTP clients — should pass
	// this through [netutil.ReloadingServerTLS] or
	// [netutil.ReloadingClientTLS] instead of constructing static
	// configs from [Config.TLS], so the whole service shares one
	// reload poll.
	TLSCertSource netutil.CertificateSource

	TenantBudget  budget.Budget    // nil if no TenantBudget
	ActionLog     actionlog.Logger // nil if no ActionLogger
	ApprovalStore approval.Store   // nil if no ApprovalStore
	Authz         kitauthz.Decider // nil if no Authz

	Storage        storage.Storage  // nil if no Storage
	StorageManager *storage.Manager // nil if no NamedStorage
	AuditLog       *auditlog.Logger // nil if no AuditLog
	EventBus       *eventbus.Bus      // always non-nil; in-process domain event dispatch

	HTTPClient *http.Client
	Config     BaseConfig

	// resources holds adapter-published handles indexed by a sub-package key.
	// Adapter modules in app/postgres, app/redis, app/amqp, app/nats, and
	// app/grpc populate this map via [Infrastructure.SetResource]; consumers
	// retrieve their typed handle via the sub-package's getter (e.g.,
	// postgres.Pool(infra), redis.Connection(infra)). The any-typed storage
	// keeps app/v2 free of pgx, go-redis, amqp, nats, grpc, and otelgrpc
	// imports.
	//
	// The map lives behind a pointer + mutex pair so Infrastructure stays
	// safe to copy by value (the RouterFunc signature takes Infrastructure
	// by value; adapter Populate calls share state with consumer Get
	// calls through the pointer).
	resources *resourceStore

	// Background registers a managed goroutine that runs until the worker
	// context is cancelled. If the function returns a non-nil error, the
	// entire service shuts down. Use this inside RouterFunc for late-bound
	// goroutines that need infrastructure references (hub, consumers, etc.).
	Background func(name string, fn func(ctx context.Context) error)

	// SetCustomReadiness overrides the auto-accumulated health checks with a
	// custom readiness handler. Call this inside RouterFunc when the service
	// needs per-component health introspection (e.g., per-observer scan state).
	SetCustomReadiness func(h http.Handler)

	// AddHealthCheck appends a DependencyCheck to the readiness probe.
	// Call this inside RouterFunc when health checks depend on infrastructure
	// created within the router (e.g., transport-specific checks).
	AddHealthCheck func(check health.DependencyCheck)
}

// resourceStore is the shared backing map for [Infrastructure.SetResource]
// and [Infrastructure.Resource]. Lives behind a pointer on Infrastructure so
// the surrounding struct stays copy-safe (the RouterFunc takes Infrastructure
// by value).
type resourceStore struct {
	mu sync.RWMutex
	m  map[string]any
}

func newResourceStore() *resourceStore {
	return &resourceStore{m: make(map[string]any)}
}

// SetResource publishes an adapter-owned handle under key so the matching
// sub-package's typed getter can hand it back to consumer code. Modules call
// this from [Module.Populate]; double-registration under the same key panics
// at startup because the resource keyspace is meant to be exclusive
// per-adapter (postgres, redis, amqp, nats, grpc).
//
// Keys are sub-package-defined string constants; use only the constants
// exported by the relevant adapter (e.g., postgres.ResourceKey).
func (i *Infrastructure) SetResource(key string, value any) {
	if key == "" {
		panic("app: SetResource requires a non-empty key")
	}
	if i.resources == nil {
		i.resources = newResourceStore()
	}
	i.resources.mu.Lock()
	defer i.resources.mu.Unlock()
	if _, exists := i.resources.m[key]; exists {
		panic("app: duplicate resource key — adapter modules must not double-register")
	}
	i.resources.m[key] = value
}

// Resource returns the adapter handle published under key. Sub-package
// getters use this to retrieve their typed value; ok=false means the
// matching adapter module was not registered with the Builder.
func (i *Infrastructure) Resource(key string) (any, bool) {
	if i == nil || i.resources == nil {
		return nil, false
	}
	i.resources.mu.RLock()
	defer i.resources.mu.RUnlock()
	v, ok := i.resources.m[key]
	return v, ok
}

// TestInfrastructure returns an Infrastructure with safe no-op defaults for
// all function fields. Use this in tests to avoid nil-pointer panics when
// testing RouterFunc implementations.
func TestInfrastructure() Infrastructure {
	return Infrastructure{
		Logger:             slog.Default(),
		EventBus:           eventbus.New(),
		HTTPClient:         httpx.NewHTTPClient(10*time.Second, nil),
		Background:         func(_ string, _ func(ctx context.Context) error) {},
		SetCustomReadiness: func(_ http.Handler) {},
		AddHealthCheck:     func(_ health.DependencyCheck) {},
	}
}
