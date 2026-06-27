package leader

import (
	"context"
	"database/sql"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/app/v2"
	"github.com/bds421/rho-kit/infra/v2/leaderelection"
	"github.com/bds421/rho-kit/observability/v2/health"
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

func TestPGAdvisory_PanicsOnNilDB(t *testing.T) {
	assert.PanicsWithValue(t, "app/leader: PGAdvisory requires a non-nil *sql.DB", func() {
		PGAdvisory(nil, "svc")
	})
}

func TestPGAdvisory_PanicsOnEmptyKey(t *testing.T) {
	var db sql.DB
	assert.PanicsWithValue(t, "app/leader: PGAdvisory requires a non-empty key", func() {
		PGAdvisory(&db, "")
	})
}

func TestPGAdvisoryFromPostgres_PanicsOnEmptyKey(t *testing.T) {
	assert.PanicsWithValue(t, "app/leader: PGAdvisoryFromPostgres requires a non-empty key", func() {
		PGAdvisoryFromPostgres("")
	})
}

type stubPostgresModule struct {
	db *sql.DB
}

func (stubPostgresModule) Name() string { return "postgres" }
func (stubPostgresModule) Init(context.Context, app.ModuleContext) error {
	return nil
}
func (stubPostgresModule) Populate(*app.Infrastructure) {}
func (stubPostgresModule) Stop(context.Context) error   { return nil }
func (stubPostgresModule) HealthChecks() []health.DependencyCheck {
	return nil
}
func (m stubPostgresModule) SQLDB() *sql.DB { return m.db }

func TestPGAdvisoryFromPostgres_RequiresPostgresModule(t *testing.T) {
	m := PGAdvisoryFromPostgres("svc")
	mc, err := app.TestModuleContext()
	require.NoError(t, err)
	err = m.Init(context.Background(), mc)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "requires postgres module")
}

func TestPGAdvisoryFromPostgres_InitWiresElector(t *testing.T) {
	var db sql.DB
	m := PGAdvisoryFromPostgres("svc")
	mc, err := app.TestModuleContext(stubPostgresModule{db: &db})
	require.NoError(t, err)

	require.NoError(t, m.Init(context.Background(), mc))

	infra := app.Infrastructure{}
	m.Populate(&infra)
	require.NotNil(t, Elector(infra))
}
