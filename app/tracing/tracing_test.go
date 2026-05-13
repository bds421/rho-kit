package tracing

import (
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/app/v2"
	"github.com/bds421/rho-kit/observability/v2/tracing"
	"github.com/bds421/rho-kit/runtime/v2/lifecycle"
)

func TestModule_Name(t *testing.T) {
	m := Module(tracing.Config{})
	assert.Equal(t, "tracing", m.Name())
}

func TestModule_PanicsOnExcessiveSampleRate(t *testing.T) {
	defer func() {
		r := recover()
		require.NotNil(t, r, "expected panic on excessive sample rate")
		assert.Contains(t, r, "SampleRate")
	}()
	_ = Module(tracing.Config{SampleRate: 0.5})
}

func TestModule_AllowsZeroEndpoint(t *testing.T) {
	// Zero endpoint = noop provider; Module should succeed.
	m := Module(tracing.Config{})
	require.NotNil(t, m)

	tm := m.(*tracingModule)
	mc := app.ModuleContext{
		Logger: slog.Default(),
		Runner: lifecycle.NewRunner(slog.Default()),
	}
	err := tm.Init(context.Background(), mc)
	require.NoError(t, err)
	t.Cleanup(func() { _ = tm.Stop(context.Background()) })

	// TracingActive depends on whether tracing.Init succeeded.
	// With empty endpoint it uses a noop and returns true.
	assert.True(t, tm.TracingActive())
}

func TestModule_ImplementsTracingProvider(t *testing.T) {
	var _ app.TracingProvider = (*tracingModule)(nil)
}
