package slo

import (
	"context"
	"log/slog"
	"math"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/app/v2"
	kitslo "github.com/bds421/rho-kit/observability/v2/slo"
)

func sampleSLO() kitslo.SLO {
	return kitslo.SLO{
		Name:      "api-error-rate",
		Type:      kitslo.TypeErrorRate,
		Threshold: 0.01,
	}
}

func TestModule_Name(t *testing.T) {
	m := Module(sampleSLO())
	assert.Equal(t, ModuleName, m.Name())
	assert.Equal(t, "slo", m.Name())
}

func TestModule_PanicsOnEmpty(t *testing.T) {
	assert.PanicsWithValue(t, "app/slo: Module requires at least one SLO", func() {
		Module()
	})
}

func TestModule_InitAndPopulate(t *testing.T) {
	m := Module(sampleSLO())
	mc := app.ModuleContext{Logger: slog.Default()}
	require.NoError(t, m.Init(context.Background(), mc))

	infra := app.Infrastructure{}
	m.Populate(&infra)

	got := Checker(infra)
	require.NotNil(t, got)
}

func TestModule_ImplementsSLOCheckerProvider(t *testing.T) {
	m := Module(sampleSLO())
	mc := app.ModuleContext{Logger: slog.Default()}
	require.NoError(t, m.Init(context.Background(), mc))

	sp, ok := m.(app.SLOCheckerProvider)
	require.True(t, ok)
	require.NotNil(t, sp.SLOChecker())
}

func TestChecker_NilWhenNotRegistered(t *testing.T) {
	infra := app.Infrastructure{}
	assert.Nil(t, Checker(infra))
}

func TestModule_HealthChecksAfterInit(t *testing.T) {
	m := Module(sampleSLO())
	mc := app.ModuleContext{Logger: slog.Default()}
	require.NoError(t, m.Init(context.Background(), mc))

	checks := m.HealthChecks()
	assert.Len(t, checks, 1, "SLO module surfaces one dependency check")
}

func TestModule_StopIsNoOp(t *testing.T) {
	m := Module(sampleSLO())
	require.NoError(t, m.Stop(context.Background()))
}

func TestWithGatherer_PanicsOnNil(t *testing.T) {
	assert.PanicsWithValue(t, "app/slo: WithGatherer requires a non-nil Gatherer", func() {
		WithGatherer(nil)
	})
}

func TestModuleWith_PanicsOnNilOption(t *testing.T) {
	assert.PanicsWithValue(t, "app/slo: Module option must not be nil", func() {
		ModuleWith([]Option{nil}, sampleSLO())
	})
}

func TestModuleWith_PanicsOnEmpty(t *testing.T) {
	assert.PanicsWithValue(t, "app/slo: Module requires at least one SLO", func() {
		ModuleWith(nil)
	})
}

// newRequestsRegistry returns a fresh registry carrying an
// http_requests_total counter with 1 of 100 requests labelled 5xx, so
// the default error-rate SLO evaluates to a concrete 0.01.
func newRequestsRegistry(t *testing.T) *prometheus.Registry {
	t.Helper()
	reg := prometheus.NewRegistry()
	counter := prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "http_requests_total",
			Help: "test counter",
		},
		[]string{"status"},
	)
	reg.MustRegister(counter)
	counter.WithLabelValues("200").Add(99)
	counter.WithLabelValues("500").Add(1)
	return reg
}

// TestModuleWith_GathererRoutesEvaluation proves the WithGatherer
// override actually points the checker at the supplied gatherer:
// the SLI series lives only in a custom registry, so a checker built
// against it reports a concrete error rate, while the default-gatherer
// path (which lacks the series) reports no-data (NaN).
func TestModuleWith_GathererRoutesEvaluation(t *testing.T) {
	reg := newRequestsRegistry(t)

	m := ModuleWith([]Option{WithGatherer(reg)}, sampleSLO())
	mc := app.ModuleContext{Logger: slog.Default()}
	require.NoError(t, m.Init(context.Background(), mc))

	checker := m.(app.SLOCheckerProvider).SLOChecker()
	require.NotNil(t, checker)

	statuses := checker.Evaluate()
	require.Len(t, statuses, 1)
	require.Falsef(t, math.IsNaN(statuses[0].Current),
		"checker must read from the supplied gatherer, got no-data")
	assert.InDelta(t, 0.01, statuses[0].Current, 1e-9)

	// The default gatherer has no such series in this test binary, so
	// the unconfigured module evaluates to NaN. This is the silent
	// no-data failure the override exists to prevent.
	def := Module(sampleSLO())
	require.NoError(t, def.Init(context.Background(), mc))
	defStatuses := def.(app.SLOCheckerProvider).SLOChecker().Evaluate()
	require.Len(t, defStatuses, 1)
	assert.Truef(t, math.IsNaN(defStatuses[0].Current),
		"default gatherer should lack the custom-registry series")
}
