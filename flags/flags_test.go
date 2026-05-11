package flags_test

import (
	"context"
	"strings"
	"testing"

	"github.com/open-feature/go-sdk/openfeature"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/core/v2/tenant"
	"github.com/bds421/rho-kit/flags/v2"
)

// uniqueName returns a per-test domain so the OpenFeature global
// registry doesn't accumulate stale providers from earlier tests in
// the same `go test` invocation. Pre-FR-033 the kit installed every
// provider against the global default, so test cases trampled each
// other; the new New(name, p) signature scopes per-domain.
func uniqueName(t *testing.T) string { return "test-" + t.Name() }

func TestNew_PanicsOnNilProvider(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on nil provider")
		}
	}()
	_, _ = flags.New(uniqueName(t), nil)
}

func TestNew_PanicsOnEmptyName(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on empty domain name")
		}
	}()
	_, _ = flags.New("", flags.NewMemoryProvider())
}

func TestNew_ReturnsClientForValidProvider(t *testing.T) {
	c, err := flags.New(uniqueName(t), flags.NewMemoryProvider())
	require.NoError(t, err)
	require.NotNil(t, c)
}

func TestNew_DomainIsolation(t *testing.T) {
	// FR-033 [HIGH]: two clients with different domains must NOT see
	// each other's flag values. Pre-fix, both providers landed on the
	// OpenFeature global so the second New() clobbered the first.
	p1 := flags.NewMemoryProvider()
	p1.SetString("color", "red")
	c1, err := flags.New("svc-a-"+t.Name(), p1)
	require.NoError(t, err)

	p2 := flags.NewMemoryProvider()
	p2.SetString("color", "blue")
	c2, err := flags.New("svc-b-"+t.Name(), p2)
	require.NoError(t, err)

	assert.Equal(t, "red", c1.String(context.Background(), "color", ""))
	assert.Equal(t, "blue", c2.String(context.Background(), "color", ""))
}

func TestClient_BoolDefault(t *testing.T) {
	c, err := flags.New(uniqueName(t), flags.NewMemoryProvider())
	require.NoError(t, err)
	assert.True(t, c.Bool(context.Background(), "missing", true))
	assert.False(t, c.Bool(context.Background(), "missing", false))
}

func TestClient_InvalidReceiverReturnsFallbackAndError(t *testing.T) {
	var c *flags.Client

	got, err := c.BoolE(context.Background(), "enabled", true)
	require.ErrorIs(t, err, flags.ErrInvalidClient)
	assert.True(t, got)
	assert.True(t, c.Bool(context.Background(), "enabled", true))
}

func TestClient_NilContextReturnsFallbackAndError(t *testing.T) {
	p := flags.NewMemoryProvider()
	p.SetBool("enabled", true)
	c, err := flags.New(uniqueName(t), p)
	require.NoError(t, err)

	got, err := c.BoolE(nil, "enabled", false) //nolint:staticcheck // Test nil-context hardening.
	require.ErrorIs(t, err, flags.ErrInvalidContext)
	assert.False(t, got)
	assert.False(t, c.Bool(nil, "enabled", false)) //nolint:staticcheck // Test nil-context hardening.
}

func TestClient_RejectsInvalidFlagKey(t *testing.T) {
	p := flags.NewMemoryProvider()
	p.SetBool("enabled", false)
	c, err := flags.New(uniqueName(t), p)
	require.NoError(t, err)

	for _, tc := range []struct {
		name string
		key  string
	}{
		{name: "empty", key: ""},
		{name: "space", key: "bad key"},
		{name: "newline", key: "bad\nkey"},
		{name: "invalid-utf8", key: string([]byte{0xff})},
		{name: "too-long", key: strings.Repeat("a", flags.MaxKeyLen+1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := c.BoolE(context.Background(), tc.key, true)
			require.ErrorIs(t, err, flags.ErrInvalidKey)
			if tc.name == "too-long" {
				assert.NotContains(t, err.Error(), "128")
				assert.NotContains(t, err.Error(), "129")
			}
			assert.True(t, got)
			assert.True(t, c.Bool(context.Background(), tc.key, true))
		})
	}
}

func TestClient_BoolFromProvider(t *testing.T) {
	p := flags.NewMemoryProvider()
	p.SetBool("kill_switch", true)
	c, err := flags.New(uniqueName(t), p)
	require.NoError(t, err)
	assert.True(t, c.Bool(context.Background(), "kill_switch", false))
}

func TestClient_StringFromProvider(t *testing.T) {
	p := flags.NewMemoryProvider()
	p.SetString("variant", "blue")
	c, err := flags.New(uniqueName(t), p)
	require.NoError(t, err)
	assert.Equal(t, "blue", c.String(context.Background(), "variant", "red"))
}

func TestClient_IntAndFloat(t *testing.T) {
	p := flags.NewMemoryProvider()
	p.SetInt("max_attempts", 5)
	p.SetFloat("ratio", 0.42)
	c, err := flags.New(uniqueName(t), p)
	require.NoError(t, err)
	assert.Equal(t, int64(5), c.Int(context.Background(), "max_attempts", 1))
	assert.InDelta(t, 0.42, c.Float(context.Background(), "ratio", 1.0), 1e-9)
}

func TestClient_TenantTargetingPropagates(t *testing.T) {
	// MemoryProvider doesn't actually do targeting, but verifying the
	// targeting key is built without panicking covers the
	// tenant.FromContext extraction path.
	p := flags.NewMemoryProvider()
	p.SetBool("rollout", true)
	c, err := flags.New(uniqueName(t), p)
	require.NoError(t, err)
	ctx := tenant.WithID(context.Background(), tenant.ID("tenant-42"))
	assert.True(t, c.Bool(ctx, "rollout", false))
}

func TestWithUserKey_AppearsInEvalCtx(t *testing.T) {
	p := flags.NewMemoryProvider()
	p.SetString("greeting", "hello")
	c, err := flags.New(uniqueName(t), p)
	require.NoError(t, err)
	ctx := flags.WithUserKey(context.Background(), "user-7")
	require.Equal(t, "hello", c.String(ctx, "greeting", ""))
}

func TestWithUserKey_InvalidSurfacesOnEvaluation(t *testing.T) {
	p := flags.NewMemoryProvider()
	p.SetBool("enabled", true)
	c, err := flags.New(uniqueName(t), p)
	require.NoError(t, err)

	ctx := flags.WithUserKey(context.Background(), "bad\nuser")
	got, err := c.BoolE(ctx, "enabled", false)
	require.ErrorIs(t, err, flags.ErrInvalidUserKey)
	assert.False(t, got)
	assert.False(t, c.Bool(ctx, "enabled", false))

	ctx = flags.WithUserKey(context.Background(), strings.Repeat("u", flags.MaxUserKeyLen+1))
	got, err = c.BoolE(ctx, "enabled", false)
	require.ErrorIs(t, err, flags.ErrInvalidUserKey)
	assert.NotContains(t, err.Error(), "256")
	assert.NotContains(t, err.Error(), "257")
	assert.False(t, got)
}

func TestWithUserKey_NilContextDoesNotPanic(t *testing.T) {
	p := flags.NewMemoryProvider()
	p.SetString("greeting", "hello")
	c, err := flags.New(uniqueName(t), p)
	require.NoError(t, err)

	ctx := flags.WithUserKey(nil, "user-7") //nolint:staticcheck // Test nil-context hardening.
	require.Equal(t, "hello", c.String(ctx, "greeting", ""))
}

func TestWithUserKey_EmptyIsNoop(t *testing.T) {
	ctx := context.Background()
	got := flags.WithUserKey(ctx, "")
	assert.Equal(t, ctx, got)
}

func TestMemoryProvider_PanicsOnInvalidKey(t *testing.T) {
	for _, tc := range []struct {
		name string
		fn   func(*flags.MemoryProvider)
	}{
		{name: "bool", fn: func(p *flags.MemoryProvider) { p.SetBool("bad key", true) }},
		{name: "string", fn: func(p *flags.MemoryProvider) { p.SetString("bad key", "v") }},
		{name: "int", fn: func(p *flags.MemoryProvider) { p.SetInt("bad key", 1) }},
		{name: "float", fn: func(p *flags.MemoryProvider) { p.SetFloat("bad key", 1) }},
		{name: "object", fn: func(p *flags.MemoryProvider) { p.SetObject("bad key", map[string]any{"ok": true}) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Panics(t, func() { tc.fn(flags.NewMemoryProvider()) })
		})
	}
}

func TestMemoryProvider_PanicDoesNotReflectInvalidKey(t *testing.T) {
	require.PanicsWithValue(t, "flags/memory: invalid key", func() {
		flags.NewMemoryProvider().SetBool("bad secret-token key", true)
	})
}

func TestMemoryProvider_SetObjectPanicDoesNotReflectKey(t *testing.T) {
	defer func() {
		rec := recover()
		require.NotNil(t, rec)
		msg, ok := rec.(string)
		require.True(t, ok, "panic must be a stable string, got %T", rec)
		assert.Equal(t, "flags/memory: SetObject value must be JSON-marshallable", msg)
		assert.NotContains(t, msg, "secret-token")
	}()

	flags.NewMemoryProvider().SetObject("feature-secret-token", map[string]any{
		"bad": make(chan int),
	})
}

func TestMemoryProvider_InvalidDirectEvaluationReturnsFallback(t *testing.T) {
	p := flags.NewMemoryProvider()

	detail := p.BooleanEvaluation(context.Background(), "bad key", true, openfeature.FlattenedContext{})
	assert.True(t, detail.Value)
	assert.Equal(t, openfeature.ErrorReason, detail.Reason)
	assert.Equal(t, openfeature.ParseErrorCode, detail.ResolutionDetail().ErrorCode)
	require.Error(t, detail.Error())
	assert.Contains(t, detail.Error().Error(), string(openfeature.ParseErrorCode))
}
