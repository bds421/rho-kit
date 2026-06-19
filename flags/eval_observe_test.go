package flags_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/open-feature/go-sdk/openfeature"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/flags/v2"
)

// errProvider is a minimal [flags.Provider] that returns a fixed
// resolution error for every evaluation. It exercises the
// provider-error path through the real OpenFeature SDK so the kit's
// finishEval / observeError plumbing (audit FR-034) is covered without
// a live vendor.
type errProvider struct {
	msg string
}

func (p errProvider) Metadata() openfeature.Metadata {
	return openfeature.Metadata{Name: "test/err-provider"}
}

func (p errProvider) Hooks() []openfeature.Hook { return nil }

func (p errProvider) resolution() openfeature.ProviderResolutionDetail {
	return openfeature.ProviderResolutionDetail{
		ResolutionError: openfeature.NewGeneralResolutionError(p.msg),
		Reason:          openfeature.ErrorReason,
	}
}

func (p errProvider) BooleanEvaluation(_ context.Context, _ string, fallback bool, _ openfeature.FlattenedContext) openfeature.BoolResolutionDetail {
	return openfeature.BoolResolutionDetail{Value: fallback, ProviderResolutionDetail: p.resolution()}
}

func (p errProvider) StringEvaluation(_ context.Context, _ string, fallback string, _ openfeature.FlattenedContext) openfeature.StringResolutionDetail {
	return openfeature.StringResolutionDetail{Value: fallback, ProviderResolutionDetail: p.resolution()}
}

func (p errProvider) FloatEvaluation(_ context.Context, _ string, fallback float64, _ openfeature.FlattenedContext) openfeature.FloatResolutionDetail {
	return openfeature.FloatResolutionDetail{Value: fallback, ProviderResolutionDetail: p.resolution()}
}

func (p errProvider) IntEvaluation(_ context.Context, _ string, fallback int64, _ openfeature.FlattenedContext) openfeature.IntResolutionDetail {
	return openfeature.IntResolutionDetail{Value: fallback, ProviderResolutionDetail: p.resolution()}
}

func (p errProvider) ObjectEvaluation(_ context.Context, _ string, fallback any, _ openfeature.FlattenedContext) openfeature.InterfaceResolutionDetail {
	return openfeature.InterfaceResolutionDetail{Value: fallback, ProviderResolutionDetail: p.resolution()}
}

// capturedErr records the arguments of the most recent error-hook
// invocation so tests can assert on them.
type capturedErr struct {
	calls atomic.Int64
	key   atomic.Value // string
	msg   atomic.Value // string
}

func (c *capturedErr) hook(key, message string, _ error) {
	c.calls.Add(1)
	c.key.Store(key)
	c.msg.Store(message)
}

func TestSetEvalErrorHook_FiresOnInvalidKey(t *testing.T) {
	p := flags.NewMemoryProvider()
	p.SetBool("enabled", true)
	c, err := flags.New(uniqueName(t), p)
	require.NoError(t, err)

	var cap capturedErr
	c.SetEvalErrorHook(cap.hook)

	// An invalid key is rejected before the provider is reached; the
	// hook must still fire so callers see the validation failure.
	got, err := c.BoolE(context.Background(), "bad key", true)
	require.ErrorIs(t, err, flags.ErrInvalidKey)
	assert.True(t, got)
	require.Equal(t, int64(1), cap.calls.Load())
	assert.Equal(t, "bad key", cap.key.Load())
}

func TestSetEvalErrorHook_FiresOnProviderErrorCode(t *testing.T) {
	c, err := flags.New(uniqueName(t), errProvider{msg: "upstream exploded"})
	require.NoError(t, err)

	var cap capturedErr
	c.SetEvalErrorHook(cap.hook)

	got, err := c.BoolE(context.Background(), "kill_switch", true)
	// fallback is returned, error is surfaced (FR-034 E-variant contract).
	assert.True(t, got)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "upstream exploded")
	require.Equal(t, int64(1), cap.calls.Load())
	assert.Equal(t, "kill_switch", cap.key.Load())
	assert.Equal(t, "upstream exploded", cap.msg.Load())
}

func TestSetEvalErrorHook_NilClearsHook(t *testing.T) {
	p := flags.NewMemoryProvider()
	c, err := flags.New(uniqueName(t), p)
	require.NoError(t, err)

	var cap capturedErr
	c.SetEvalErrorHook(cap.hook)
	c.SetEvalErrorHook(nil)

	_, err = c.BoolE(context.Background(), "bad key", true)
	require.ErrorIs(t, err, flags.ErrInvalidKey)
	assert.Equal(t, int64(0), cap.calls.Load(), "cleared hook must not fire")
}

func TestSetEvalErrorHook_NotFiredOnSuccessfulEval(t *testing.T) {
	p := flags.NewMemoryProvider()
	p.SetBool("enabled", true)
	c, err := flags.New(uniqueName(t), p)
	require.NoError(t, err)

	var cap capturedErr
	c.SetEvalErrorHook(cap.hook)

	got, err := c.BoolE(context.Background(), "enabled", false)
	require.NoError(t, err)
	assert.True(t, got)
	assert.Equal(t, int64(0), cap.calls.Load(), "hook must not fire on success")
}

// TestBoolE_SurfacesProviderError documents the FR-034 contract that
// the E-variants must report a provider error even when the SDK's err
// is derived purely from the resolution detail. Guards finishEval's
// error synthesis.
func TestBoolE_SurfacesProviderError(t *testing.T) {
	c, err := flags.New(uniqueName(t), errProvider{msg: "boom"})
	require.NoError(t, err)

	got, gotErr := c.BoolE(context.Background(), "flag", false)
	assert.False(t, got, "fallback returned on provider error")
	require.Error(t, gotErr)
	assert.Contains(t, gotErr.Error(), "boom")
}

func TestClient_ObjectRoundTrip(t *testing.T) {
	p := flags.NewMemoryProvider()
	p.SetObject("config", map[string]any{"feature": "on"})
	c, err := flags.New(uniqueName(t), p)
	require.NoError(t, err)

	got := c.Object(context.Background(), "config", map[string]any{})
	gotMap, ok := got.(map[string]any)
	require.True(t, ok, "expected map[string]any, got %T", got)
	assert.Equal(t, "on", gotMap["feature"])
}

func TestClient_ObjectE_ReturnsFallbackAndErrorOnInvalidKey(t *testing.T) {
	p := flags.NewMemoryProvider()
	c, err := flags.New(uniqueName(t), p)
	require.NoError(t, err)

	fallback := map[string]any{"default": true}
	got, err := c.ObjectE(context.Background(), "bad key", fallback)
	require.ErrorIs(t, err, flags.ErrInvalidKey)
	assert.Equal(t, fallback, got)
}

func TestGenericObjectE_TypeMismatchReturnsError(t *testing.T) {
	p := flags.NewMemoryProvider()
	// Store an object shape, then ask for a string.
	p.SetObject("config", map[string]any{"k": "v"})
	c, err := flags.New(uniqueName(t), p)
	require.NoError(t, err)

	got, err := flags.ObjectE[string](c, context.Background(), "config", "fallback")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "config")
	assert.Contains(t, err.Error(), "want string")
	assert.Equal(t, "fallback", got)
}

func TestGenericObject_FallsBackOnTypeMismatch(t *testing.T) {
	p := flags.NewMemoryProvider()
	p.SetObject("config", map[string]any{"k": "v"})
	c, err := flags.New(uniqueName(t), p)
	require.NoError(t, err)

	// Object (no E) swallows the mismatch and returns the fallback.
	got := flags.Object[string](c, context.Background(), "config", "fallback")
	assert.Equal(t, "fallback", got)
}

func TestGenericObjectE_SuccessReturnsTypedValue(t *testing.T) {
	p := flags.NewMemoryProvider()
	p.SetObject("config", map[string]any{"feature": "on"})
	c, err := flags.New(uniqueName(t), p)
	require.NoError(t, err)

	got, err := flags.ObjectE[map[string]any](c, context.Background(), "config", nil)
	require.NoError(t, err)
	assert.Equal(t, "on", got["feature"])
}

func TestGenericObjectE_PropagatesEvalError(t *testing.T) {
	c, err := flags.New(uniqueName(t), errProvider{msg: "provider down"})
	require.NoError(t, err)

	got, gotErr := flags.ObjectE[map[string]any](c, context.Background(), "config", map[string]any{"fb": true})
	require.Error(t, gotErr)
	// On eval error the generic helper returns the fallback, not a
	// type-mismatch error.
	assert.False(t, errors.Is(gotErr, flags.ErrInvalidKey))
	assert.Equal(t, map[string]any{"fb": true}, got)
}
