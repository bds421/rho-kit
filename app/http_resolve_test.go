package app

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestResolveHTTPConfig_NoProviderReturnsZeroValue confirms the kit
// defaults (zero value) apply when no HTTPConfigProvider is registered.
func TestResolveHTTPConfig_NoProviderReturnsZeroValue(t *testing.T) {
	got := resolveHTTPConfig([]Module{NewBaseModule("plain")})
	assert.Equal(t, resolvedHTTPConfig{}, got)
}

// TestResolveHTTPConfig_SingleProviderWins confirms a single provider's
// values are threaded through unchanged.
func TestResolveHTTPConfig_SingleProviderWins(t *testing.T) {
	got := resolveHTTPConfig([]Module{
		NewBaseModule("plain"),
		&stubHTTPConfig{BaseModule: NewBaseModule("http"), plaintext: true},
	})
	assert.True(t, got.allowPlaintext, "single provider's AllowPlaintext must be honored")
}

// TestResolveHTTPConfig_PanicsOnMultipleProviders pins the fail-fast
// contract: two modules implementing HTTPConfigProvider is a startup
// misconfiguration. Without this guard the "first wins" rule resolves
// to different providers across Run (reordered modules) versus Validate
// / serverTLSOptions (registration order).
func TestResolveHTTPConfig_PanicsOnMultipleProviders(t *testing.T) {
	msg := capturePanic(t, func() {
		resolveHTTPConfig([]Module{
			&stubHTTPConfig{BaseModule: NewBaseModule("http-a"), plaintext: true},
			&stubHTTPConfig{BaseModule: NewBaseModule("http-b")},
		})
	})
	assert.Contains(t, msg, "HTTPConfigProvider", "panic must name the offending capability")
}

// TestBuilder_PanicsOnTwoHTTPConfigProviders exercises the same guard
// through the public Builder surface (distinct module names so the
// name-dedup check does not fire first).
func TestBuilder_PanicsOnTwoHTTPConfigProviders(t *testing.T) {
	assert.Panics(t, func() {
		New("test-svc", "v0.1.0", validBaseConfig()).
			With(&stubHTTPConfig{BaseModule: NewBaseModule("http-a"), plaintext: true}).
			With(&stubHTTPConfig{BaseModule: NewBaseModule("http-b"), plaintext: true}).
			WithoutRateLimit().
			Validate()
	})
}
