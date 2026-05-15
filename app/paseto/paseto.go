// Package paseto is the lazy app-module wrapper for
// [github.com/bds421/rho-kit/crypto/v2/paseto].
//
// Services that want PASETO v4.public verification register
// [paseto.Module] on the Builder. Services that don't, do not
// import this package — and therefore don't pull the PASETO SDK
// (aidanwoods.dev/go-paseto + decred secp256k1) into their
// binary, matching the way app/amqp, app/grpc, app/nats,
// app/redis, app/postgres, app/tracing, and app/flags keep their
// respective heavy SDKs out of core app/v2.
//
// Retrieve the constructed Provider inside the [app.RouterFunc]
// via [Provider]:
//
//	app.New(name).
//	    With(paseto.Module(p)).
//	    Run(func(infra app.Infrastructure) http.Handler {
//	        provider := paseto.Provider(infra)
//	        ...
//	    })
package paseto

import (
	"context"

	"github.com/bds421/rho-kit/app/v2"
	kitpaseto "github.com/bds421/rho-kit/crypto/v2/paseto"
	"github.com/bds421/rho-kit/observability/v2/health"
)

// ResourceProviderKey is the [app.Infrastructure.Resource] key
// under which [Module] publishes its *kitpaseto.Provider. Tests
// and adapter introspection code can read it directly;
// application code should use [Provider].
const ResourceProviderKey = "github.com/bds421/rho-kit/app/paseto.provider"

// moduleName is the identifier registered with the Builder and
// surfaced in module-ordering errors / lifecycle logs.
const moduleName = "paseto"

// Module returns an [app.Module] that wires the supplied PASETO
// Provider into the kit lifecycle: the background key-refresh
// goroutine continues running until shutdown, and the Provider is
// published on [app.Infrastructure] under [ResourceProviderKey].
//
// The Provider is caller-constructed (via [kitpaseto.OpenProvider]
// or equivalent) because PASETO key rotation is service-specific —
// the kit deliberately doesn't ship a "default" PASETO source the
// way it does for JWT.
//
// Panics if provider is nil.
func Module(provider *kitpaseto.Provider) app.Module {
	if provider == nil {
		panic("app/paseto: Module requires a non-nil Provider")
	}
	return &pasetoModule{provider: provider}
}

type pasetoModule struct {
	provider *kitpaseto.Provider
}

func (m *pasetoModule) Name() string { return moduleName }

func (m *pasetoModule) Init(_ context.Context, mc app.ModuleContext) error {
	// The Provider's refresh loop was started inside OpenProvider.
	// We only need to ensure Close is called on shutdown so the
	// goroutine terminates with the lifecycle Runner.
	mc.Runner.AddFunc("paseto-provider", func(ctx context.Context) error {
		<-ctx.Done()
		return m.provider.Close()
	})
	mc.Logger.Info("paseto provider wired")
	return nil
}

func (m *pasetoModule) Populate(infra *app.Infrastructure) {
	infra.SetResource(ResourceProviderKey, m.provider)
}

func (m *pasetoModule) Stop(_ context.Context) error { return nil }

func (m *pasetoModule) HealthChecks() []health.DependencyCheck { return nil }

// Provider returns the *kitpaseto.Provider published by [Module]
// under [ResourceProviderKey], or nil if [Module] was not
// registered with the Builder.
func Provider(infra app.Infrastructure) *kitpaseto.Provider {
	v, ok := infra.Resource(ResourceProviderKey)
	if !ok {
		return nil
	}
	p, _ := v.(*kitpaseto.Provider)
	return p
}
