package flags_test

import (
	"sync/atomic"
	"testing"

	"github.com/open-feature/go-sdk/openfeature"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/flags/v2"
)

// shutdownProbe is a Provider that also implements openfeature.StateHandler,
// so the OpenFeature SDK invokes Shutdown() on it during flags.Shutdown().
// It records whether that hook fired.
type shutdownProbe struct {
	*flags.MemoryProvider
	called atomic.Bool
}

func (p *shutdownProbe) Init(openfeature.EvaluationContext) error { return nil }

func (p *shutdownProbe) Shutdown() { p.called.Store(true) }

// TestShutdownDrainsProvider verifies flags.Shutdown drains the installed
// provider — flushing buffered analytics and stopping background goroutines
// (LaunchDarkly, flagd streaming) — instead of leaking it. This is the
// behaviour app/flags wires into the lifecycle Runner; the probe test lives
// here because the abstraction module is the one permitted to import the
// OpenFeature SDK (dependency-boundary gate).
//
// Not parallel: flags.Shutdown resets the process-global OpenFeature SDK
// singleton, so it must not race other tests' provider installs.
func TestShutdownDrainsProvider(t *testing.T) {
	probe := &shutdownProbe{MemoryProvider: flags.NewMemoryProvider()}
	_, err := flags.New(uniqueName(t), probe)
	require.NoError(t, err)
	require.False(t, probe.called.Load(), "provider must not be shut down before flags.Shutdown")

	flags.Shutdown()

	require.True(t, probe.called.Load(), "flags.Shutdown must drain the installed OpenFeature provider")
}
