package ratelimit

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/app/v2"
)

func newMC(t *testing.T) app.ModuleContext {
	t.Helper()
	mc, err := app.TestModuleContext()
	require.NoError(t, err)
	return mc
}

func TestIP_Name(t *testing.T) {
	m := IP(100, time.Minute)
	assert.Equal(t, "ratelimit-ip", m.Name())
}

func TestIP_PanicsOnInvalid(t *testing.T) {
	assert.PanicsWithValue(t, "app/ratelimit: IP requires a positive request limit", func() {
		IP(0, time.Minute)
	})
	assert.PanicsWithValue(t, "app/ratelimit: IP requires a positive window", func() {
		IP(10, 0)
	})
}

func TestKeyed_Name(t *testing.T) {
	m := Keyed("api", 100, time.Minute)
	assert.Equal(t, "ratelimit-keyed-api", m.Name())
}

func TestKeyed_PanicsOnInvalid(t *testing.T) {
	assert.PanicsWithValue(t, "app/ratelimit: Keyed requires a non-empty name", func() {
		Keyed("", 10, time.Minute)
	})
	assert.PanicsWithValue(t, "app/ratelimit: Keyed requires a positive request limit", func() {
		Keyed("api", 0, time.Minute)
	})
	assert.PanicsWithValue(t, "app/ratelimit: Keyed requires a positive window", func() {
		Keyed("api", 10, 0)
	})
}

func TestKeyed_PanicsOnNonMetricSafeName(t *testing.T) {
	assert.Panics(t, func() {
		Keyed("api with spaces", 10, time.Minute)
	}, "metric-unsafe characters in the keyed name must panic")
}

func TestIP_ImplementsRateLimitDeclarer(t *testing.T) {
	m := IP(100, time.Minute)
	_, ok := m.(app.RateLimitDeclarer)
	assert.True(t, ok, "IP module must satisfy RateLimitDeclarer for the Builder gate")
}

func TestKeyed_DoesNotImplementRateLimitDeclarer(t *testing.T) {
	m := Keyed("api", 10, time.Minute)
	_, ok := m.(app.RateLimitDeclarer)
	assert.False(t, ok, "Keyed must NOT satisfy RateLimitDeclarer — it installs no mux-wide middleware")
}

func TestIP_InitAndPopulate(t *testing.T) {
	m := IP(100, time.Minute)
	require.NoError(t, m.Init(context.Background(), newMC(t)))

	infra := app.Infrastructure{}
	m.Populate(&infra)

	assert.NotNil(t, IPLimiter(infra), "IPLimiter(infra) must return the limiter")
}

func TestIP_ImplementsMiddlewareInstaller(t *testing.T) {
	m := IP(100, time.Minute)
	require.NoError(t, m.Init(context.Background(), newMC(t)))

	mi, ok := m.(app.MiddlewareInstaller)
	require.True(t, ok)
	mws := mi.PublicMiddleware()
	require.Len(t, mws, 1)
	assert.Equal(t, app.PhaseRateLimit, mws[0].Phase)
}

func TestKeyed_InitAndPopulate(t *testing.T) {
	m := Keyed("api", 10, time.Minute)
	require.NoError(t, m.Init(context.Background(), newMC(t)))

	infra := app.Infrastructure{}
	m.Populate(&infra)

	got := KeyedLimiter(infra, "api")
	require.NotNil(t, got)
	// Wrong name returns nil.
	assert.Nil(t, KeyedLimiter(infra, "other"))
}

func TestKeyed_StacksUnderSharedResourceMap(t *testing.T) {
	m1 := Keyed("api", 10, time.Minute)
	m2 := Keyed("admin", 5, time.Minute)
	mc := newMC(t)
	require.NoError(t, m1.Init(context.Background(), mc))
	require.NoError(t, m2.Init(context.Background(), mc))

	infra := app.Infrastructure{}
	m1.Populate(&infra)
	m2.Populate(&infra)

	assert.NotNil(t, KeyedLimiter(infra, "api"))
	assert.NotNil(t, KeyedLimiter(infra, "admin"))
}

func TestLimiters_NilWhenNotRegistered(t *testing.T) {
	infra := app.Infrastructure{}
	assert.Nil(t, IPLimiter(infra))
	assert.Nil(t, KeyedLimiter(infra, "anything"))
}

func TestIP_WithTrustedProxies(t *testing.T) {
	m := IP(100, time.Minute, WithTrustedProxies([]string{"10.0.0.0/8"}))
	require.NoError(t, m.Init(context.Background(), newMC(t)))
	infra := app.Infrastructure{}
	m.Populate(&infra)
	assert.NotNil(t, IPLimiter(infra))
}

func TestIP_WithTrustedProxies_PanicsOnInvalid(t *testing.T) {
	assert.Panics(t, func() {
		IP(100, time.Minute, WithTrustedProxies([]string{"not-a-cidr"}))
	})
}
