// Package slo is the lazy app-module wrapper for
// [github.com/bds421/rho-kit/observability/v2/slo]. Services that
// want SLO monitoring pass [slo.Module] to [app.Builder.With];
// services that don't, do not import this package.
//
// The Module installs:
//   - an SLO checker reading from prometheus.DefaultGatherer (override
//     via [ModuleWith] + [WithGatherer] for services on a custom registry)
//   - a dependency-check that reports SLO breaches on /readyz
//   - a JSON /slo handler on the internal-ops server (via the
//     [SLOCheckerProvider] capability)
//
// Retrieve the checker inside the [app.RouterFunc] via [Checker]:
//
//	app.New(name).
//	    With(slo.Module(mySLOs...)).
//	    Run(func(infra app.Infrastructure) http.Handler { ... })
package slo

import (
	"context"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/bds421/rho-kit/app/v2"
	"github.com/bds421/rho-kit/observability/v2/health"
	"github.com/bds421/rho-kit/observability/v2/slo"
)

// ResourceCheckerKey is the [app.Infrastructure.Resource] key
// under which [Module] publishes its *slo.Checker.
const ResourceCheckerKey = "github.com/bds421/rho-kit/app/slo.checker"

// ModuleName is the registered Module.Name() value.
const ModuleName = "slo"

// Option configures [Module]/[ModuleWith].
type Option func(*config)

type config struct {
	gatherer prometheus.Gatherer
}

// WithGatherer overrides the [prometheus.Gatherer] the SLO checker
// reads from. Default is [prometheus.DefaultGatherer].
//
// Use this when the service routes its HTTP/SLI metrics to a custom
// registry rather than the default one; otherwise SLO checks are
// evaluated against a gatherer that lacks the series, so breaches can
// never be detected and /slo silently reports no-data.
//
// Panics if g is nil.
func WithGatherer(g prometheus.Gatherer) Option {
	if g == nil {
		panic("app/slo: WithGatherer requires a non-nil Gatherer")
	}
	return func(c *config) { c.gatherer = g }
}

// Module returns an [app.Module] that wires SLO monitoring. The
// SLO list is copied at construction time so later mutation of
// the slice has no effect.
//
// The checker reads from [prometheus.DefaultGatherer]; use
// [ModuleWith] with [WithGatherer] to point it at a custom registry.
//
// Panics if no SLOs are supplied — registering the module with an
// empty list is almost always a programming mistake.
func Module(slos ...slo.SLO) app.Module {
	return ModuleWith(nil, slos...)
}

// ModuleOpts is the kit-conventional constructor: required SLOs as a
// slice, then variadic [Option]s (mirrors postgres.Module(cfg, opts...),
// amqp.Module(url, opts...), …). Prefer this over [ModuleWith].
//
// Panics if no SLOs are supplied, or if any option is nil.
func ModuleOpts(slos []slo.SLO, opts ...Option) app.Module {
	return ModuleWith(opts, slos...)
}

// ModuleWith returns an [app.Module] that wires SLO monitoring,
// applying the supplied [Option]s. Prefer [ModuleOpts] for the
// kit-wide required-then-variadic-options shape; this form keeps the
// historical opts-slice-first signature for existing callers.
//
// Panics if no SLOs are supplied, or if any option is nil.
func ModuleWith(opts []Option, slos ...slo.SLO) app.Module {
	if len(slos) == 0 {
		panic("app/slo: Module requires at least one SLO")
	}
	cfg := config{gatherer: prometheus.DefaultGatherer}
	for _, opt := range opts {
		if opt == nil {
			panic("app/slo: Module option must not be nil")
		}
		opt(&cfg)
	}
	return &sloModule{
		slos:     append([]slo.SLO(nil), slos...),
		gatherer: cfg.gatherer,
	}
}

type sloModule struct {
	slos     []slo.SLO
	gatherer prometheus.Gatherer
	checker  *slo.Checker
}

func (m *sloModule) Name() string { return ModuleName }

func (m *sloModule) Init(_ context.Context, mc app.ModuleContext) error {
	gatherer := m.gatherer
	if gatherer == nil {
		gatherer = prometheus.DefaultGatherer
	}
	m.checker = slo.NewChecker(gatherer, m.slos...)
	mc.Logger.Info("slo checker initialized", "slo_count", len(m.slos))
	return nil
}

func (m *sloModule) Populate(infra *app.Infrastructure) {
	if m.checker != nil {
		infra.SetResource(ResourceCheckerKey, m.checker)
	}
}

func (m *sloModule) Stop(_ context.Context) error { return nil }

func (m *sloModule) HealthChecks() []health.DependencyCheck {
	if m.checker == nil {
		return nil
	}
	return []health.DependencyCheck{m.checker.DependencyCheck()}
}

// SLOChecker implements [app.SLOCheckerProvider] so the Builder
// can wire the internal-ops /slo handler without importing this
// package.
func (m *sloModule) SLOChecker() *slo.Checker { return m.checker }

// Checker returns the SLO checker published by [Module] under
// [ResourceCheckerKey], or nil if [Module] was not registered.
func Checker(infra app.Infrastructure) *slo.Checker {
	v, ok := infra.Resource(ResourceCheckerKey)
	if !ok {
		return nil
	}
	c, _ := v.(*slo.Checker)
	return c
}
