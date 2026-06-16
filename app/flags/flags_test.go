package flags

import (
	"context"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/open-feature/go-sdk/openfeature"
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

// shutdownProbeProvider is a Provider that also implements
// openfeature.StateHandler so the SDK calls Shutdown() on it during
// openfeature.Shutdown(). It records whether that hook fired, which is
// how the test verifies the module drains the provider on lifecycle
// shutdown instead of leaking its background goroutines / buffered
// analytics.
type shutdownProbeProvider struct {
	*kitflags.MemoryProvider
	shutdownCalled atomic.Bool
}

func (p *shutdownProbeProvider) Init(openfeature.EvaluationContext) error { return nil }

func (p *shutdownProbeProvider) Shutdown() { p.shutdownCalled.Store(true) }

// TestModule_ShutsDownProviderOnLifecycleStop wires the module into a
// real lifecycle Runner, runs it, then cancels the parent context to
// trigger graceful shutdown. The provider's Shutdown hook MUST fire —
// otherwise providers with background goroutines and buffered analytics
// (LaunchDarkly, flagd streaming) neither drain nor flush during the
// kit's otherwise-careful graceful shutdown.
//
// Not parallel: openfeature.Shutdown resets the global SDK singleton,
// so this test must not race the other tests' provider installs.
func TestModule_ShutsDownProviderOnLifecycleStop(t *testing.T) {
	probe := &shutdownProbeProvider{MemoryProvider: kitflags.NewMemoryProvider()}
	m := Module(probe)

	logger := slog.Default()
	runner := lifecycle.NewRunner(logger, lifecycle.WithStopTimeout(2*time.Second))
	mc := app.ModuleContext{ServiceName: "shutdown-test", Logger: logger, Runner: runner}
	require.NoError(t, m.Init(context.Background(), mc))

	require.False(t, probe.shutdownCalled.Load(),
		"provider must not be shut down before lifecycle stop")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runner.Run(ctx) }()

	// Give the runner time to start the registered shutdown component.
	time.Sleep(50 * time.Millisecond)
	require.False(t, probe.shutdownCalled.Load(),
		"provider must stay up while the service is running")

	cancel()
	require.NoError(t, <-done)

	assert.True(t, probe.shutdownCalled.Load(),
		"Init must register a shutdown that drains the OpenFeature provider")
}

func TestModule_StopIsNoOp(t *testing.T) {
	m := Module(kitflags.NewMemoryProvider())
	require.NoError(t, m.Init(context.Background(), newMC(t)))
	require.NoError(t, m.Stop(context.Background()))
}
