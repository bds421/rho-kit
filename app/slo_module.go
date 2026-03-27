package app

import (
	"context"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/bds421/rho-kit/observability/health"
	"github.com/bds421/rho-kit/observability/slo"
)

// sloModule implements the Module interface for SLO monitoring.
type sloModule struct {
	BaseModule
	slos []slo.SLO

	// initialized during Init
	checker *slo.Checker
}

// newSLOModule creates an SLO module with the given SLO definitions.
// Panics if no SLOs are provided (startup-time configuration error).
func newSLOModule(slos ...slo.SLO) *sloModule {
	if len(slos) == 0 {
		panic("app: WithSLO requires at least one SLO")
	}
	return &sloModule{
		BaseModule: NewBaseModule("slo"),
		slos:       slos,
	}
}

func (m *sloModule) Init(_ context.Context, mc ModuleContext) error {
	m.checker = slo.NewChecker(prometheus.DefaultGatherer, m.slos...)
	mc.Logger.Info("slo checker initialized", "slo_count", len(m.slos))
	return nil
}

func (m *sloModule) HealthChecks() []health.DependencyCheck {
	if m.checker == nil {
		return nil
	}
	return []health.DependencyCheck{m.checker.DependencyCheck()}
}

// Checker returns the initialized SLO checker. Returns nil if Init has not
// been called. Used by the builder to wire the /slo endpoint.
func (m *sloModule) Checker() *slo.Checker {
	return m.checker
}
