package flags

import (
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/app/v2"
	kitflags "github.com/bds421/rho-kit/flags/v2"
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
	m := Module(kitflags.NewMemoryProvider())
	assert.Equal(t, ModuleName, m.Name())
	assert.Equal(t, "flags", m.Name())
}

func TestModule_PanicsOnNilProvider(t *testing.T) {
	assert.PanicsWithValue(t, "app/flags: Module requires a non-nil Provider", func() {
		Module(nil)
	})
}

func TestModule_PopulatePublishesClient(t *testing.T) {
	m := Module(kitflags.NewMemoryProvider())
	require.NoError(t, m.Init(context.Background(), newMC(t)))

	infra := app.Infrastructure{}
	m.Populate(&infra)

	got := Client(infra)
	require.NotNil(t, got, "Client(infra) must return the wrapped *kitflags.Client")
}

func TestClient_NilWhenNotRegistered(t *testing.T) {
	infra := app.Infrastructure{}
	assert.Nil(t, Client(infra))
}

// The module wires kitflags.Shutdown into the lifecycle Runner so the
// OpenFeature provider drains on graceful shutdown. That wiring is
// verified end-to-end in the flags abstraction's own test
// (flags.TestShutdownDrainsProvider), which lives in the adapter module
// that is permitted to import the OpenFeature SDK — this app module must
// not (dependency-boundary gate), so the probe-provider test was moved
// there rather than importing openfeature here.

func TestModule_StopIsNoOp(t *testing.T) {
	m := Module(kitflags.NewMemoryProvider())
	require.NoError(t, m.Init(context.Background(), newMC(t)))
	require.NoError(t, m.Stop(context.Background()))
}
