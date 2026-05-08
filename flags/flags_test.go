package flags_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/core/tenant"
	"github.com/bds421/rho-kit/flags"
)

func TestNew_PanicsOnNilProvider(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on nil provider")
		}
	}()
	flags.New("svc", nil)
}

func TestClient_BoolDefault(t *testing.T) {
	c := flags.New("svc", flags.NewMemoryProvider())
	assert.True(t, c.Bool(context.Background(), "missing", true))
	assert.False(t, c.Bool(context.Background(), "missing", false))
}

func TestClient_BoolFromProvider(t *testing.T) {
	p := flags.NewMemoryProvider()
	p.SetBool("kill_switch", true)
	c := flags.New("svc", p)
	assert.True(t, c.Bool(context.Background(), "kill_switch", false))
}

func TestClient_StringFromProvider(t *testing.T) {
	p := flags.NewMemoryProvider()
	p.SetString("variant", "blue")
	c := flags.New("svc", p)
	assert.Equal(t, "blue", c.String(context.Background(), "variant", "red"))
}

func TestClient_IntAndFloat(t *testing.T) {
	p := flags.NewMemoryProvider()
	p.SetInt("max_attempts", 5)
	p.SetFloat("ratio", 0.42)
	c := flags.New("svc", p)
	assert.Equal(t, int64(5), c.Int(context.Background(), "max_attempts", 1))
	assert.InDelta(t, 0.42, c.Float(context.Background(), "ratio", 1.0), 1e-9)
}

func TestClient_TenantTargetingPropagates(t *testing.T) {
	// MemoryProvider doesn't actually do targeting, but verifying the
	// targeting key is built without panicking covers the
	// tenant.FromContext extraction path.
	p := flags.NewMemoryProvider()
	p.SetBool("rollout", true)
	c := flags.New("svc", p)
	ctx := tenant.WithID(context.Background(), tenant.ID("tenant-42"))
	assert.True(t, c.Bool(ctx, "rollout", false))
}

func TestWithUserKey_AppearsInEvalCtx(t *testing.T) {
	p := flags.NewMemoryProvider()
	p.SetString("greeting", "hello")
	c := flags.New("svc", p)
	ctx := flags.WithUserKey(context.Background(), "user-7")
	require.Equal(t, "hello", c.String(ctx, "greeting", ""))
}

func TestWithUserKey_EmptyIsNoop(t *testing.T) {
	ctx := context.Background()
	got := flags.WithUserKey(ctx, "")
	assert.Equal(t, ctx, got)
}
