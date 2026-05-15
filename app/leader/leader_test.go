package leader

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/app/v2"
	"github.com/bds421/rho-kit/infra/v2/leaderelection"
)

type stubElector struct{}

func (stubElector) Run(_ context.Context, _ leaderelection.Callbacks) error { return nil }
func (stubElector) IsLeader() bool                                          { return false }

func TestModule_Name(t *testing.T) {
	m := Module(stubElector{})
	assert.Equal(t, ModuleName, m.Name())
	assert.Equal(t, "leader-election", m.Name())
}

func TestModule_PanicsOnNil(t *testing.T) {
	assert.PanicsWithValue(t, "app/leader: Module requires a non-nil Elector", func() {
		Module(nil)
	})
}

func TestModule_PopulatePublishesElector(t *testing.T) {
	want := stubElector{}
	m := Module(want)

	infra := app.Infrastructure{}
	m.Populate(&infra)

	got := Elector(infra)
	require.NotNil(t, got)
	_, ok := got.(stubElector)
	assert.True(t, ok)
}

func TestModule_ImplementsElectorProvider(t *testing.T) {
	want := stubElector{}
	m := Module(want)
	ep, ok := m.(app.ElectorProvider)
	require.True(t, ok, "leaderModule must implement ElectorProvider")
	got := ep.Elector()
	_, isStub := got.(stubElector)
	assert.True(t, isStub)
}

func TestElector_NilWhenNotRegistered(t *testing.T) {
	infra := app.Infrastructure{}
	assert.Nil(t, Elector(infra))
}

func TestModule_StopIsNoOp(t *testing.T) {
	m := Module(stubElector{})
	require.NoError(t, m.Stop(context.Background()))
}

func TestModule_HealthChecksEmpty(t *testing.T) {
	m := Module(stubElector{})
	assert.Empty(t, m.HealthChecks())
}
