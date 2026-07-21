package authz

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestScope_RegisterValid(t *testing.T) {
	resetScopesForTest()
	defer resetScopesForTest()

	got, err := Register("users.read", "List users")
	require.NoError(t, err)
	assert.Equal(t, Scope("users.read"), got)
	assert.True(t, IsRegistered("users.read"))
}

func TestScope_RegisterRejectsBadName(t *testing.T) {
	resetScopesForTest()
	defer resetScopesForTest()

	_, err := Register("Users.Read", "bad case")
	assert.Error(t, err, "uppercase rejected")

	_, err = Register("users read", "bad space")
	assert.Error(t, err, "space rejected")

	_, err = Register("", "empty name")
	assert.Error(t, err)
}

func TestScope_RegisterRejectsEmptyDescription(t *testing.T) {
	resetScopesForTest()
	defer resetScopesForTest()

	_, err := Register("users.read", "")
	assert.Error(t, err, "empty description rejected for OpenAPI generation")
}

func TestScope_ReRegisterSameDescriptionIsNoop(t *testing.T) {
	resetScopesForTest()
	defer resetScopesForTest()

	_, err := Register("users.read", "List users")
	require.NoError(t, err)
	_, err = Register("users.read", "List users")
	assert.NoError(t, err, "same description is idempotent")
}

func TestScope_ReRegisterDifferentDescriptionFails(t *testing.T) {
	resetScopesForTest()
	defer resetScopesForTest()

	_, err := Register("users.read", "List users")
	require.NoError(t, err)
	_, err = Register("users.read", "Read users")
	assert.Error(t, err, "different description must fail to surface drift")
}

func TestScope_RegisteredScopesSorted(t *testing.T) {
	resetScopesForTest()
	defer resetScopesForTest()

	MustRegister("users.write", "Write users")
	MustRegister("admin.all", "Admin")
	MustRegister("users.read", "List users")

	entries := RegisteredScopes()
	require.Len(t, entries, 3)
	assert.Equal(t, Scope("admin.all"), entries[0].Scope)
	assert.Equal(t, Scope("users.read"), entries[1].Scope)
	assert.Equal(t, Scope("users.write"), entries[2].Scope)
}

func TestScope_MustRegisterPanicsOnInvalid(t *testing.T) {
	resetScopesForTest()
	defer resetScopesForTest()

	assert.Panics(t, func() { MustRegister("Bad Name", "x") })
}

func TestRegistry_IsolatedFromDefault(t *testing.T) {
	resetScopesForTest()
	defer resetScopesForTest()

	r := NewRegistry()
	_, err := r.Register("isolated.read", "Isolated")
	require.NoError(t, err)
	assert.True(t, r.IsRegistered("isolated.read"))
	assert.False(t, IsRegistered("isolated.read"), "instance must not pollute DefaultRegistry")

	ResetScopes()
	assert.False(t, IsRegistered("isolated.read"))
	assert.True(t, r.IsRegistered("isolated.read"), "ResetScopes only clears DefaultRegistry")
	r.Reset()
	assert.False(t, r.IsRegistered("isolated.read"))
}
