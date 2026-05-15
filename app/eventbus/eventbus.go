// Package eventbus is the lazy app-module wrapper for the kit's
// in-process domain-event bus
// ([github.com/bds421/rho-kit/runtime/v2/eventbus]). Services pass
// [eventbus.Module] to [app.Builder.With] to provision a Bus that
// publishes/subscribes within the process, then access it inside
// the router via [eventbus.Bus].
//
//	app.New(name, ver, cfg).
//	    With(eventbus.Module(eventbus.WithPoolSize(8))).
//	    Router(func(infra app.Infrastructure) http.Handler {
//	        bus := eventbus.Bus(infra)
//	        // typed publish / subscribe via the runtime/v2/eventbus
//	        // package: eventbus.Subscribe(bus, fn) / eventbus.Publish(bus, ctx, ev)
//	        return router(infra, bus)
//	    }).
//	    Run()
//
// The bridge keeps the runtime/v2/eventbus import out of services
// that don't register one — consistent with waves 88-98.
package eventbus

import (
	"context"
	"log/slog"

	"github.com/bds421/rho-kit/app/v2"
	"github.com/bds421/rho-kit/observability/v2/health"
	kiteventbus "github.com/bds421/rho-kit/runtime/v2/eventbus"
)

// ModuleName is the registered Module.Name() value.
const ModuleName = "eventbus"

// ResourceBusKey is the [app.Infrastructure.Resource] key under
// which [Module] publishes the *eventbus.Bus.
const ResourceBusKey = "github.com/bds421/rho-kit/app/eventbus.bus"

// Option configures [Module].
type Option func(*config)

type config struct {
	poolSize int
	logger   *slog.Logger
}

// WithPoolSize overrides the default bounded worker-pool size. The
// pool drains subscribers in parallel during shutdown.
//
// Panics if size <= 0.
func WithPoolSize(size int) Option {
	if size <= 0 {
		panic("app/eventbus: WithPoolSize requires a positive size")
	}
	return func(c *config) { c.poolSize = size }
}

// WithLogger overrides the logger the underlying bus uses for
// dispatch errors and dropped-event diagnostics. Defaults to the
// kit's service logger.
func WithLogger(l *slog.Logger) Option {
	return func(c *config) { c.logger = l }
}

// Module returns an [app.Module] that constructs an in-process
// [kiteventbus.Bus], attaches it to the lifecycle runner so the
// worker pool drains on shutdown, and publishes the bus on
// [app.Infrastructure] under [ResourceBusKey].
//
// Panics if any opt is nil.
func Module(opts ...Option) app.Module {
	cfg := config{}
	for _, opt := range opts {
		if opt == nil {
			panic("app/eventbus: Module option must not be nil")
		}
		opt(&cfg)
	}
	return &eventbusModule{cfg: cfg}
}

type eventbusModule struct {
	cfg config
	bus *kiteventbus.Bus
}

func (m *eventbusModule) Name() string { return ModuleName }

func (m *eventbusModule) Init(_ context.Context, mc app.ModuleContext) error {
	logger := m.cfg.logger
	if logger == nil {
		logger = mc.Logger
	}
	busOpts := []kiteventbus.Option{kiteventbus.WithLogger(logger)}
	if m.cfg.poolSize > 0 {
		busOpts = append(busOpts, kiteventbus.WithWorkerPool(m.cfg.poolSize))
	}
	m.bus = kiteventbus.New(busOpts...)
	mc.Runner.Add("eventbus", m.bus)
	return nil
}

func (m *eventbusModule) Populate(infra *app.Infrastructure) {
	if m.bus != nil {
		infra.SetResource(ResourceBusKey, m.bus)
	}
}

// Stop is a no-op; the worker pool is drained via the lifecycle
// Runner.Add registration in Init.
func (m *eventbusModule) Stop(_ context.Context) error { return nil }

func (m *eventbusModule) HealthChecks() []health.DependencyCheck { return nil }

// Bus returns the in-process event bus published by [Module], or
// nil if no module was registered.
func Bus(infra app.Infrastructure) *kiteventbus.Bus {
	v, ok := infra.Resource(ResourceBusKey)
	if !ok {
		return nil
	}
	b, _ := v.(*kiteventbus.Bus)
	return b
}
