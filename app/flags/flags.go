// Package flags is the lazy app-module wrapper for
// [github.com/bds421/rho-kit/flags/v2].
//
// Services that want OpenFeature-backed feature flags pass
// [flags.Module] to [app.Builder.With]. Services that don't, do
// not import this package — and therefore don't pull
// github.com/open-feature/go-sdk into their binary, matching the
// way app/amqp, app/grpc, app/nats, app/redis, app/postgres, and
// app/tracing keep their respective heavy SDKs out of core
// app/v2.
//
// Retrieve the constructed client inside the [app.RouterFunc] via
// [Client]:
//
//	app.New(name).
//	    With(flags.Module(provider)).
//	    Run(func(infra app.Infrastructure) http.Handler {
//	        client := flags.Client(infra)
//	        ...
//	    })
package flags

import (
	"context"

	"github.com/open-feature/go-sdk/openfeature"

	"github.com/bds421/rho-kit/app/v2"
	kitflags "github.com/bds421/rho-kit/flags/v2"
	"github.com/bds421/rho-kit/observability/v2/health"
)

// ResourceClientKey is the [app.Infrastructure.Resource] key under
// which [Module] publishes its *kitflags.Client. Tests and
// adapter introspection code can read it directly; application
// code should use [Client].
const ResourceClientKey = "github.com/bds421/rho-kit/app/flags.client"

// ModuleName is the identifier registered with the Builder and
// surfaced in module-ordering errors / lifecycle logs.
const ModuleName = "flags"

// Module returns an [app.Module] that wraps provider in the kit's
// [kitflags.Client] at Init time and publishes the client on
// [app.Infrastructure] under [ResourceClientKey].
//
// Provider construction failures (network, auth, malformed config)
// surface as a Builder Init error so the service exits non-zero
// instead of running with a silent best-effort no-op client.
//
// Panics if provider is nil.
func Module(provider kitflags.Provider) app.Module {
	if provider == nil {
		panic("app/flags: Module requires a non-nil Provider")
	}
	return &flagsModule{provider: provider}
}

type flagsModule struct {
	provider kitflags.Provider

	// initialized during Init.
	client *kitflags.Client
}

func (m *flagsModule) Name() string { return ModuleName }

func (m *flagsModule) Init(_ context.Context, mc app.ModuleContext) error {
	// kitflags.New returns an error on validation failure (empty
	// service name, nil provider). We surface that as the module's
	// Init error so the lifecycle Runner treats it as a startup
	// abort, not a silently-degraded runtime.
	client, err := kitflags.New(mc.ServiceName, m.provider)
	if err != nil {
		return err
	}
	m.client = client

	// kitflags.New installed the provider via
	// openfeature.SetNamedProviderAndWait. The OpenFeature SDK owns
	// the provider's lifecycle once installed, so we hand its
	// shutdown to the lifecycle Runner instead of leaving it leaking:
	// providers with background goroutines and buffered analytics
	// (LaunchDarkly, flagd streaming) drain and flush only when their
	// Shutdown hook fires. Mirrors app/paseto, which wires
	// provider.Close into the Runner the same way.
	//
	// openfeature.Shutdown resets the whole SDK API (this version has
	// no per-domain shutdown). That matches the kit contract of one
	// flags.Module per service: the module is the SDK's sole owner, so
	// a process-terminating reset is the correct teardown. The Runner
	// invokes this in reverse registration order on SIGINT/SIGTERM.
	mc.Runner.AddFunc("flags-provider", func(ctx context.Context) error {
		<-ctx.Done()
		openfeature.Shutdown()
		return nil
	})
	return nil
}

func (m *flagsModule) Populate(infra *app.Infrastructure) {
	if m.client != nil {
		infra.SetResource(ResourceClientKey, m.client)
	}
}

// Stop is a no-op: provider shutdown is driven by the lifecycle
// Runner func registered in Init ("flags-provider"), not from here,
// so it participates in the Runner's shared stop budget and ordering.
func (m *flagsModule) Stop(_ context.Context) error { return nil }

func (m *flagsModule) HealthChecks() []health.DependencyCheck { return nil }

// Client returns the *kitflags.Client published by [Module] under
// [ResourceClientKey], or nil if [Module] was not registered with
// the Builder. Use the nil safely — kitflags.Client methods
// recognise a nil receiver as "no flags configured" and return the
// caller-supplied default.
func Client(infra app.Infrastructure) *kitflags.Client {
	v, ok := infra.Resource(ResourceClientKey)
	if !ok {
		return nil
	}
	c, _ := v.(*kitflags.Client)
	return c
}
