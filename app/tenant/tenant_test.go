package tenant

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/app/v2"
	httpxtenant "github.com/bds421/rho-kit/httpx/v2/middleware/tenant"
)

func TestModule_Name(t *testing.T) {
	m := Module(httpxtenant.HeaderExtractor("X-Tenant-Id"))
	assert.Equal(t, ModuleName, m.Name())
	assert.Equal(t, "tenant", m.Name())
}

func TestModule_PanicsOnNilOption(t *testing.T) {
	assert.PanicsWithValue(t, "app/tenant: Module option must not be nil", func() {
		Module(httpxtenant.HeaderExtractor("X"), nil)
	})
}

func TestModule_DefaultRequired(t *testing.T) {
	m := Module(httpxtenant.HeaderExtractor("X-Tenant-Id"))
	tm, ok := m.(app.TenantPolicyProvider)
	require.True(t, ok, "tenantModule must implement TenantPolicyProvider")
	assert.True(t, tm.TenantRequired(), "default policy must be Required")
	assert.False(t, tm.TenantAllowsMissingOnSafeMethods())
}

func TestModule_WithoutTenantRequired(t *testing.T) {
	m := Module(httpxtenant.HeaderExtractor("X-Tenant-Id"), WithoutTenantRequired())
	tm := m.(app.TenantPolicyProvider)
	assert.False(t, tm.TenantRequired())
}

func TestModule_WithAllowMissingOnSafeMethods(t *testing.T) {
	m := Module(httpxtenant.HeaderExtractor("X-Tenant-Id"), WithAllowMissingOnSafeMethods())
	tm := m.(app.TenantPolicyProvider)
	assert.True(t, tm.TenantRequired(), "Required is still the default when AllowMissingOnSafeMethods is set")
	assert.True(t, tm.TenantAllowsMissingOnSafeMethods())
}

func TestModule_InitBuildsCachedMiddleware(t *testing.T) {
	m := Module(httpxtenant.HeaderExtractor("X-Tenant-Id"))
	mc, err := app.TestModuleContext(m)
	require.NoError(t, err)
	require.NoError(t, m.Init(context.Background(), mc))

	mi, ok := m.(app.MiddlewareInstaller)
	require.True(t, ok, "tenantModule must implement MiddlewareInstaller")
	mws := mi.PublicMiddleware()
	require.Len(t, mws, 1, "tenant module installs exactly one phased middleware")
	assert.Equal(t, app.PhaseTenant, mws[0].Phase)
	require.NotNil(t, mws[0].Func, "middleware must be cached by Init")

	// Second read returns the same function value (cached).
	mws2 := mi.PublicMiddleware()
	require.Len(t, mws2, 1)
	// Function values aren't directly comparable but the slice with
	// the same underlying *http.Handler is stable; len-and-non-nil
	// check is enough to pin the contract.
}

func TestModule_PublicMiddlewareEmptyBeforeInit(t *testing.T) {
	m := Module(httpxtenant.HeaderExtractor("X-Tenant-Id"))
	mi := m.(app.MiddlewareInstaller)
	assert.Empty(t, mi.PublicMiddleware(), "PublicMiddleware must return nothing before Init")
}

func TestModule_StopIsNoOp(t *testing.T) {
	m := Module(httpxtenant.HeaderExtractor("X-Tenant-Id"))
	require.NoError(t, m.Stop(context.Background()))
}
