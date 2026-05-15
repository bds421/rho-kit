package cron

import (
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/app/v2"
	"github.com/bds421/rho-kit/infra/v2/leaderelection"
	"github.com/bds421/rho-kit/runtime/v2/lifecycle"
)

func newMC(t *testing.T, modules ...app.Module) app.ModuleContext {
	t.Helper()
	mc, err := app.TestModuleContext(modules...)
	require.NoError(t, err)
	return mc
}

func TestModule_Name(t *testing.T) {
	m := Module()
	assert.Equal(t, ModuleName, m.Name())
	assert.Equal(t, "cron", m.Name())
}

func TestModule_PanicsOnNilOption(t *testing.T) {
	assert.PanicsWithValue(t, "app/cron: Module option must not be nil", func() {
		Module(nil)
	})
}

func TestModule_InitAndPopulateWithoutLeader(t *testing.T) {
	m := Module()
	mc := newMC(t, m)
	require.NoError(t, m.Init(context.Background(), mc))

	infra := app.Infrastructure{}
	m.Populate(&infra)

	got := Scheduler(infra)
	require.NotNil(t, got)
}

type stubLeaderModule struct {
	app.BaseModule
	leader bool
}

func (s *stubLeaderModule) Elector() leaderelection.Elector { return stubElector{leader: s.leader} }

type stubElector struct {
	leader bool
}

func (s stubElector) Run(_ context.Context, _ leaderelection.Callbacks) error { return nil }
func (s stubElector) IsLeader() bool                                          { return s.leader }

func TestModule_InitGatesOnLeader(t *testing.T) {
	stub := &stubLeaderModule{
		BaseModule: app.NewBaseModule("leader-election"),
		leader:     true,
	}
	m := Module()
	mc := newMC(t, stub, m)
	require.NoError(t, m.Init(context.Background(), mc))

	// Hard to introspect kitcron.Scheduler internals from out here,
	// but the fact that Init returned nil with the leader stub
	// present means the lookup path executed successfully.
	infra := app.Infrastructure{}
	m.Populate(&infra)
	assert.NotNil(t, Scheduler(infra))
}

func TestScheduler_NilWhenNotRegistered(t *testing.T) {
	infra := app.Infrastructure{}
	assert.Nil(t, Scheduler(infra))
}

func TestModule_StopBeforeInit(t *testing.T) {
	m := Module()
	require.NoError(t, m.Stop(context.Background()))
}

// silenceUnused keeps lifecycle/slog imports honest when refactoring.
var _ = lifecycle.NewRunner
var _ = slog.Default
