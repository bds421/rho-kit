package budget

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/app/v2"
	apptenant "github.com/bds421/rho-kit/app/tenant/v2"
	budgetmem "github.com/bds421/rho-kit/data/v2/budget/memory"
	httpxtenant "github.com/bds421/rho-kit/httpx/v2/middleware/tenant"
)

func testBudget(t *testing.T) *budgetmem.Budget {
	t.Helper()
	b := budgetmem.New(1000, time.Hour, budgetmem.WithoutSweeper())
	t.Cleanup(func() { _ = b.Close() })
	return b
}

func TestModule_Name(t *testing.T) {
	m := Module(testBudget(t))
	assert.Equal(t, ModuleName, m.Name())
	assert.Equal(t, "budget", m.Name())
}

func TestModule_PanicsOnNilStore(t *testing.T) {
	assert.PanicsWithValue(t, "app/budget: Module requires a non-nil budget store", func() {
		Module(nil)
	})
}

func TestModule_PanicsOnNilOption(t *testing.T) {
	assert.PanicsWithValue(t, "app/budget: Module option must not be nil", func() {
		Module(testBudget(t), nil)
	})
}

// withModules builds a ModuleContext whose moduleMap is pre-populated
// with the given modules — mirrors how the Builder threads its
// modules map into Init in production.
func withModules(t *testing.T, modules ...app.Module) app.ModuleContext {
	t.Helper()
	mc, err := app.TestModuleContext(modules...)
	require.NoError(t, err)
	return mc
}

func TestInit_FailsWithoutTenant(t *testing.T) {
	m := Module(testBudget(t))
	mc := withModules(t)
	err := m.Init(context.Background(), mc)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "tenant.Module")
}

func TestInit_FailsWhenTenantOptional(t *testing.T) {
	tm := apptenant.Module(httpxtenant.HeaderExtractor("X-Tenant-Id"), apptenant.WithoutTenantRequired())
	m := Module(testBudget(t))
	mc := withModules(t, tm, m)
	err := m.Init(context.Background(), mc)
	require.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "tenant.WithoutTenantRequired") ||
		strings.Contains(err.Error(), "Required"), "got: %v", err)
}

func TestInit_FailsWhenTenantAllowsMissingOnSafeMethods(t *testing.T) {
	tm := apptenant.Module(
		httpxtenant.HeaderExtractor("X-Tenant-Id"),
		apptenant.WithAllowMissingOnSafeMethods(),
	)
	m := Module(testBudget(t))
	mc := withModules(t, tm, m)
	err := m.Init(context.Background(), mc)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "WithAllowMissingOnSafeMethods")
}

func TestInit_PassesWithRequiredTenant(t *testing.T) {
	tm := apptenant.Module(httpxtenant.HeaderExtractor("X-Tenant-Id"))
	m := Module(testBudget(t))
	mc := withModules(t, tm, m)
	require.NoError(t, m.Init(context.Background(), mc))
}

func TestPopulate_PublishesStore(t *testing.T) {
	b := testBudget(t)
	tm := apptenant.Module(httpxtenant.HeaderExtractor("X-Tenant-Id"))
	m := Module(b)
	mc := withModules(t, tm, m)
	require.NoError(t, m.Init(context.Background(), mc))

	infra := app.Infrastructure{}
	m.Populate(&infra)
	got := Store(infra)
	require.NotNil(t, got)
	assert.Equal(t, b, got)
}

func TestStore_NilWhenNotRegistered(t *testing.T) {
	infra := app.Infrastructure{}
	assert.Nil(t, Store(infra))
}
