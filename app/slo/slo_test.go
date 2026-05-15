package slo

import (
	"context"
	"log/slog"
	"testing"

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
