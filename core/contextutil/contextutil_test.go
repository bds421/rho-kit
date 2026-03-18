package contextutil_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/core/contextutil"
)

var testStringKey contextutil.Key[string]

func TestKey_SetGet(t *testing.T) {
	ctx := testStringKey.Set(context.Background(), "hello")

	val, ok := testStringKey.Get(ctx)
	require.True(t, ok)
	assert.Equal(t, "hello", val)
}

func TestKey_Missing(t *testing.T) {
	val, ok := testStringKey.Get(context.Background())
	assert.False(t, ok)
	assert.Zero(t, val)
}

func TestKey_MustGet_Panics(t *testing.T) {
	assert.PanicsWithValue(t, "contextutil: Key[string] not found in context; ensure the value was set upstream and named types are used to avoid collisions", func() {
		testStringKey.MustGet(context.Background())
	})
}

func TestKey_SameType_SameKey(t *testing.T) {
	// Two Key[string] variables share the same context slot by design.
	// Use named types to distinguish values of the same underlying type.
	var keyA contextutil.Key[string]
	var keyB contextutil.Key[string]

	ctx := keyA.Set(context.Background(), "from-A")
	ctx = keyB.Set(ctx, "from-B") // overwrites "from-A"

	val, ok := keyA.Get(ctx)
	require.True(t, ok)
	assert.Equal(t, "from-B", val) // same slot
}

// Named types provide distinct keys for the same underlying type.
type userID string
type sessionID string

func TestKey_NamedTypes_NoCollision(t *testing.T) {
	var userKey contextutil.Key[userID]
	var sessionKey contextutil.Key[sessionID]

	ctx := userKey.Set(context.Background(), "user-123")
	ctx = sessionKey.Set(ctx, "session-456")

	u, uOk := userKey.Get(ctx)
	s, sOk := sessionKey.Get(ctx)

	require.True(t, uOk)
	require.True(t, sOk)
	assert.Equal(t, userID("user-123"), u)
	assert.Equal(t, sessionID("session-456"), s)
}

func TestKey_DifferentTypes(t *testing.T) {
	var strKey contextutil.Key[string]
	var intKey contextutil.Key[int]

	ctx := strKey.Set(context.Background(), "text")
	ctx = intKey.Set(ctx, 42)

	strVal, strOk := strKey.Get(ctx)
	intVal, intOk := intKey.Get(ctx)

	require.True(t, strOk)
	require.True(t, intOk)
	assert.Equal(t, "text", strVal)
	assert.Equal(t, 42, intVal)
}
