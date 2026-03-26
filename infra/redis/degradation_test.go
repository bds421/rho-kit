package redis

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPassthroughPolicy_Name(t *testing.T) {
	p := PassthroughPolicy{}
	assert.Equal(t, "passthrough", p.Name())
}

func TestPassthroughPolicy_OnUnavailable(t *testing.T) {
	p := PassthroughPolicy{}
	err := p.OnUnavailable(context.Background())
	assert.NoError(t, err)
}

func TestFailFastPolicy_Name(t *testing.T) {
	p := FailFastPolicy{}
	assert.Equal(t, "fail-fast", p.Name())
}

func TestFailFastPolicy_OnUnavailable(t *testing.T) {
	p := FailFastPolicy{}
	err := p.OnUnavailable(context.Background())
	assert.ErrorIs(t, err, ErrUnavailable)
}

func TestCustomPolicy_Name(t *testing.T) {
	p := NewCustomPolicy("my-policy", func(_ context.Context) error { return nil })
	assert.Equal(t, "my-policy", p.Name())
}

func TestCustomPolicy_OnUnavailable(t *testing.T) {
	sentinel := errors.New("custom error")
	p := NewCustomPolicy("custom", func(_ context.Context) error { return sentinel })
	err := p.OnUnavailable(context.Background())
	assert.ErrorIs(t, err, sentinel)
}

func TestCustomPolicy_OnUnavailable_Nil(t *testing.T) {
	p := NewCustomPolicy("passlike", func(_ context.Context) error { return nil })
	err := p.OnUnavailable(context.Background())
	assert.NoError(t, err)
}

func TestNewCustomPolicy_PanicsOnEmptyName(t *testing.T) {
	assert.Panics(t, func() {
		NewCustomPolicy("", func(_ context.Context) error { return nil })
	})
}

func TestNewCustomPolicy_PanicsOnInvalidName(t *testing.T) {
	assert.Panics(t, func() {
		NewCustomPolicy("INVALID NAME!", func(_ context.Context) error { return nil })
	})
}

func TestNewCustomPolicy_PanicsOnNilFunc(t *testing.T) {
	assert.Panics(t, func() {
		NewCustomPolicy("valid-name", nil)
	})
}

func TestPerFeatureHealthChecks_Healthy(t *testing.T) {
	conn := newTestConnection(true, true)
	features := []FeatureCheck{
		{Feature: "cache", Policy: PassthroughPolicy{}},
		{Feature: "locks", Policy: FailFastPolicy{}},
	}

	checks := PerFeatureHealthChecks(conn, features)
	require.Len(t, checks, 2)

	assert.Equal(t, "redis-cache", checks[0].Name)
	assert.Equal(t, "healthy", checks[0].Check(context.Background()))
	assert.False(t, checks[0].Critical)

	assert.Equal(t, "redis-locks", checks[1].Name)
	assert.Equal(t, "healthy", checks[1].Check(context.Background()))
	assert.True(t, checks[1].Critical)
}

func TestPerFeatureHealthChecks_Unhealthy_Passthrough(t *testing.T) {
	conn := newTestConnection(false, true)
	features := []FeatureCheck{
		{Feature: "cache", Policy: PassthroughPolicy{}},
	}

	checks := PerFeatureHealthChecks(conn, features)
	require.Len(t, checks, 1)

	assert.Equal(t, "degraded", checks[0].Check(context.Background()))
	assert.False(t, checks[0].Critical)
}

func TestPerFeatureHealthChecks_Unhealthy_FailFast(t *testing.T) {
	conn := newTestConnection(false, true)
	features := []FeatureCheck{
		{Feature: "locks", Policy: FailFastPolicy{}},
	}

	checks := PerFeatureHealthChecks(conn, features)
	require.Len(t, checks, 1)

	assert.Equal(t, "unhealthy", checks[0].Check(context.Background()))
	assert.True(t, checks[0].Critical)
}

func TestPerFeatureHealthChecks_Connecting(t *testing.T) {
	conn := newTestConnection(false, false)
	features := []FeatureCheck{
		{Feature: "cache", Policy: PassthroughPolicy{}},
		{Feature: "locks", Policy: FailFastPolicy{}},
	}

	checks := PerFeatureHealthChecks(conn, features)
	require.Len(t, checks, 2)

	assert.Equal(t, "connecting", checks[0].Check(context.Background()))
	assert.Equal(t, "connecting", checks[1].Check(context.Background()))
}

func TestPerFeatureHealthChecks_Empty(t *testing.T) {
	conn := newTestConnection(true, true)
	checks := PerFeatureHealthChecks(conn, nil)
	assert.Empty(t, checks)
}

func TestPerFeatureHealthChecks_CustomPolicy(t *testing.T) {
	conn := newTestConnection(false, true)
	custom := NewCustomPolicy("custom-fallback", func(_ context.Context) error { return nil })
	features := []FeatureCheck{
		{Feature: "rate-limit", Policy: custom},
	}

	checks := PerFeatureHealthChecks(conn, features)
	require.Len(t, checks, 1)

	// Custom policies that are not FailFastPolicy report degraded.
	assert.Equal(t, "redis-rate-limit", checks[0].Name)
	assert.Equal(t, "degraded", checks[0].Check(context.Background()))
	assert.False(t, checks[0].Critical)
}

func TestPerFeatureHealthChecks_PanicsOnInvalidFeatureName(t *testing.T) {
	conn := newTestConnection(true, true)
	features := []FeatureCheck{
		{Feature: "INVALID NAME!", Policy: PassthroughPolicy{}},
	}

	assert.Panics(t, func() {
		PerFeatureHealthChecks(conn, features)
	})
}

func TestPerFeatureHealthChecks_PanicsOnEmptyFeatureName(t *testing.T) {
	conn := newTestConnection(true, true)
	features := []FeatureCheck{
		{Feature: "", Policy: PassthroughPolicy{}},
	}

	assert.Panics(t, func() {
		PerFeatureHealthChecks(conn, features)
	})
}

func TestPerFeatureHealthChecks_PanicsOnNilConnection(t *testing.T) {
	features := []FeatureCheck{
		{Feature: "cache", Policy: PassthroughPolicy{}},
	}

	assert.Panics(t, func() {
		PerFeatureHealthChecks(nil, features)
	})
}

func TestDegradationPolicy_Interface(t *testing.T) {
	// Verify all policies implement the interface at compile time.
	var _ DegradationPolicy = PassthroughPolicy{}
	var _ DegradationPolicy = FailFastPolicy{}
	var _ DegradationPolicy = CustomPolicy{}
}
