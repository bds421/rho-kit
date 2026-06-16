package tenant

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	coretenant "github.com/bds421/rho-kit/core/v2/tenant"
)

func TestNewScope_Valid(t *testing.T) {
	id := coretenant.MustNewID("tenant-1")
	scope, err := NewScope(id)
	require.NoError(t, err)
	assert.Equal(t, id, scope.ID())
	assert.False(t, scope.IsZero())
}

func TestNewScope_ZeroIDRejected(t *testing.T) {
	_, err := NewScope("")
	assert.ErrorIs(t, err, ErrAnonymousScope)
}

func TestMustNewScope_PanicsOnZero(t *testing.T) {
	assert.Panics(t, func() { MustNewScope("") })
}

func TestFromContext_NoTenantReturnsAnonymous(t *testing.T) {
	_, err := FromContext(context.Background())
	assert.ErrorIs(t, err, ErrAnonymousScope)
}

func TestFromContext_PicksUpBoundID(t *testing.T) {
	id := coretenant.MustNewID("tenant-2")
	ctx, err := coretenant.WithID(context.Background(), id)
	require.NoError(t, err)

	scope, err := FromContext(ctx)
	require.NoError(t, err)
	assert.Equal(t, id, scope.ID())
}

func TestScope_WhereClause(t *testing.T) {
	scope := MustNewScope(coretenant.MustNewID("tenant-3"))

	clause, arg := scope.WhereClause(2)
	assert.Equal(t, "tenant_id = $3", clause, "placeholder must be currentArgCount+1")
	assert.Equal(t, "tenant-3", arg)

	// First argument case.
	clause, arg = scope.WhereClause(0)
	assert.Equal(t, "tenant_id = $1", clause)
	assert.Equal(t, "tenant-3", arg)
}

func TestScope_WhereClause_PanicsOnNegativeCount(t *testing.T) {
	scope := MustNewScope(coretenant.MustNewID("tenant-3"))
	// A negative current arg count is a programmer error: there is no
	// such thing as a query with fewer than zero existing placeholders.
	// Fail loud rather than silently emitting an out-of-range "$0"/"$-1"
	// that only surfaces later as an obscure pgx error at query time.
	assert.Panics(t, func() { scope.WhereClause(-1) })
}

func TestScope_Key(t *testing.T) {
	scope := MustNewScope(coretenant.MustNewID("tenant-4"))
	key, err := scope.Key("budget", "weekly")
	require.NoError(t, err)
	assert.Contains(t, key, "tenant-4")
	assert.Contains(t, key, "budget")
	assert.Contains(t, key, "weekly")
}

func TestScope_Key_RejectsBadPart(t *testing.T) {
	scope := MustNewScope(coretenant.MustNewID("tenant-5"))
	// Contains a control character — coretenant.KeyFor must reject.
	_, err := scope.Key("bad\x00key")
	assert.Error(t, err)
}

func TestScope_ZeroValue(t *testing.T) {
	var zero Scope
	assert.True(t, zero.IsZero())
}

func TestErrAnonymousScope_IsCheckable(t *testing.T) {
	_, err := NewScope("")
	// errors.Is must work for the sentinel.
	assert.True(t, errors.Is(err, ErrAnonymousScope))
}
