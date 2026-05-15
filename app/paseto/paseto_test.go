package paseto

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/app/v2"
	kitpaseto "github.com/bds421/rho-kit/crypto/v2/paseto"
)

func TestModule_Name(t *testing.T) {
	// A Provider that was never opened — Init / Stop is not exercised
	// here, only the construction-time + Populate-time contracts.
	m := Module(&kitpaseto.Provider{})
	assert.Equal(t, ModuleName, m.Name())
	assert.Equal(t, "paseto", m.Name())
}

func TestModule_PanicsOnNilProvider(t *testing.T) {
	assert.PanicsWithValue(t, "app/paseto: Module requires a non-nil Provider", func() {
		Module(nil)
	})
}

func TestModule_PopulatePublishesProvider(t *testing.T) {
	want := &kitpaseto.Provider{}
	m := Module(want)

	infra := app.Infrastructure{}
	m.Populate(&infra)

	got := Provider(infra)
	require.NotNil(t, got, "Provider(infra) must return the registered Provider")
	assert.Same(t, want, got)
}

func TestProvider_NilWhenNotRegistered(t *testing.T) {
	infra := app.Infrastructure{}
	assert.Nil(t, Provider(infra))
}
