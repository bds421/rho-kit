package contextutil_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/core/v2/contextutil"
)

var testStringKey = contextutil.NewKey[string]("test-string")

func TestKey_SetGet(t *testing.T) {
	ctx := testStringKey.Set(context.Background(), "hello")

	val, ok := testStringKey.Get(ctx)
	require.True(t, ok)
	assert.Equal(t, "hello", val)
}

func TestKey_SetNilContextUsesBackground(t *testing.T) {
	//nolint:staticcheck // Deliberately verifies normalization of nil context inputs.
	ctx := testStringKey.Set(nil, "hello")
	require.NotNil(t, ctx)

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
	assert.PanicsWithValue(t, "contextutil: required key is not present in context; ensure the value was set upstream", func() {
		testStringKey.MustGet(context.Background())
	})
}

func TestKey_MustGetPanicDoesNotReflectName(t *testing.T) {
	key := contextutil.NewKey[string]("secret-token")
	assert.PanicsWithValue(t, "contextutil: required key is not present in context; ensure the value was set upstream", func() {
		key.MustGet(context.Background())
	})
}

func TestKey_SameType_DistinctIdentity(t *testing.T) {
	// Two NewKey[string] calls produce distinct context keys, even
	// though they share the same type parameter — this is the v2
	// guarantee that closes off the cross-package collision footgun.
	keyA := contextutil.NewKey[string]("A")
	keyB := contextutil.NewKey[string]("B")

	ctx := keyA.Set(context.Background(), "from-A")
	ctx = keyB.Set(ctx, "from-B")

	a, aOK := keyA.Get(ctx)
	b, bOK := keyB.Get(ctx)
	require.True(t, aOK)
	require.True(t, bOK)
	assert.Equal(t, "from-A", a)
	assert.Equal(t, "from-B", b)
}

func TestKey_DifferentTypes(t *testing.T) {
	strKey := contextutil.NewKey[string]("str")
	intKey := contextutil.NewKey[int]("int")

	ctx := strKey.Set(context.Background(), "text")
	ctx = intKey.Set(ctx, 42)

	strVal, strOk := strKey.Get(ctx)
	intVal, intOk := intKey.Get(ctx)

	require.True(t, strOk)
	require.True(t, intOk)
	assert.Equal(t, "text", strVal)
	assert.Equal(t, 42, intVal)
}

func TestKey_ZeroValue_PanicsOnSet(t *testing.T) {
	var unconstructed contextutil.Key[string]
	assert.Panics(t, func() {
		unconstructed.Set(context.Background(), "x")
	})
}

func TestKey_ZeroValue_GetReturnsFalse(t *testing.T) {
	var unconstructed contextutil.Key[string]
	v, ok := unconstructed.Get(context.Background())
	assert.False(t, ok)
	assert.Zero(t, v)
}
