// Package slo is the lazy app-module wrapper for
// [github.com/bds421/rho-kit/observability/v2/slo]. Services that
// want SLO monitoring pass [slo.Module] to [app.Builder.With];
// services that don't, do not import this package.
//
// The Module installs:
//   - an SLO checker reading from prometheus.DefaultGatherer
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

// Module returns an [app.Module] that wires SLO monitoring. The
// SLO list is copied at construction time so later mutation of
// the slice has no effect.
//
// Panics if no SLOs are supplied — registering the module with an
// empty list is almost always a programming mistake.
func Module(slos ...slo.SLO) app.Module {
	if len(slos) == 0 {
		panic("app/slo: Module requires at least one SLO")
	}
	return &sloModule{slos: append([]slo.SLO(nil), slos...)}
}

type sloModule struct {
	slos    []slo.SLO
	checker *slo.Checker
}

func (m *sloModule) Name() string { return ModuleName }

func (m *sloModule) Init(_ context.Context, mc app.ModuleContext) error {
	m.checker = slo.NewChecker(prometheus.DefaultGatherer, m.slos...)
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
