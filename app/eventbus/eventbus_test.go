package eventbus

import (
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/app/v2"
	"github.com/bds421/rho-kit/runtime/v2/lifecycle"
)

func newMC(t *testing.T) app.ModuleContext {
	t.Helper()
	logger := slog.Default()
	return app.ModuleContext{
		ServiceName: "test",
		Logger:      logger,
		Runner:      lifecycle.NewRunner(logger),
	}
}

func TestModule_Name(t *testing.T) {
	m := Module()
	assert.Equal(t, ModuleName, m.Name())
	assert.Equal(t, "eventbus", m.Name())
}

func TestWithPoolSize_PanicsOnZero(t *testing.T) {
	assert.PanicsWithValue(t, "app/eventbus: WithPoolSize requires a positive size", func() {
		WithPoolSize(0)
	})
}

func TestWithPoolSize_PanicsOnNegative(t *testing.T) {
	assert.PanicsWithValue(t, "app/eventbus: WithPoolSize requires a positive size", func() {
		WithPoolSize(-1)
	})
}

func TestModule_PanicsOnNilOption(t *testing.T) {
	assert.PanicsWithValue(t, "app/eventbus: Module option must not be nil", func() {
		Module(nil)
	})
}

func TestModule_InitAndPopulate(t *testing.T) {
	m := Module(WithPoolSize(4))
	require.NoError(t, m.Init(context.Background(), newMC(t)))

	infra := app.Infrastructure{}
	m.Populate(&infra)

	got := Bus(infra)
	require.NotNil(t, got, "Bus(infra) must return the constructed bus")
}

func TestModule_InitWithCustomLogger(t *testing.T) {
	m := Module(WithLogger(slog.Default()))
	require.NoError(t, m.Init(context.Background(), newMC(t)))

	infra := app.Infrastructure{}
	m.Populate(&infra)
	require.NotNil(t, Bus(infra))
}

func TestBus_NilWhenNotRegistered(t *testing.T) {
	infra := app.Infrastructure{}
	assert.Nil(t, Bus(infra))
}

func TestModule_StopIsNoOp(t *testing.T) {
	m := Module()
	require.NoError(t, m.Init(context.Background(), newMC(t)))
	require.NoError(t, m.Stop(context.Background()))
}
