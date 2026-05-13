package app

import (
	"context"
	"crypto/tls"
	"testing"

	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	kitredis "github.com/bds421/rho-kit/infra/redis/v2"
)

func TestNewRedisModule_PanicsOnNilOpts(t *testing.T) {
	defer func() {
		r := recover()
		require.NotNil(t, r, "expected panic for nil redis options")
		assert.Contains(t, r, "must not be nil")
	}()
	newRedisModule(nil, true)
}

func TestRedisModule_Name(t *testing.T) {
	m := newRedisModule(&goredis.Options{Addr: "localhost:6379"}, true)
	assert.Equal(t, "redis", m.Name())
}

func TestNewRedisModule_ClonesOptionsAndConnOptions(t *testing.T) {
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS10, NextProtos: []string{"h2"}, ServerName: "before.example"}
	opts := &goredis.Options{Addr: "localhost:6379", TLSConfig: tlsConfig}
	connOpts := []kitredis.ConnOption{kitredis.WithInstance("primary")}

	m := newRedisModule(opts, true, connOpts...)
	opts.Addr = "mutated:6379"
	tlsConfig.ServerName = "after.example"
	tlsConfig.NextProtos[0] = "http/1.1"
	connOpts[0] = nil

	assert.Equal(t, "localhost:6379", m.opts.Addr)
	require.NotNil(t, m.opts.TLSConfig)
	assert.Equal(t, uint16(tls.VersionTLS12), m.opts.TLSConfig.MinVersion)
	assert.Equal(t, []string{"h2"}, m.opts.TLSConfig.NextProtos)
	assert.Equal(t, "before.example", m.opts.TLSConfig.ServerName)
	assert.NotSame(t, tlsConfig, m.opts.TLSConfig)
	require.Len(t, m.connOpts, 1)
	assert.NotNil(t, m.connOpts[0])
}

func TestNewRedisModule_PanicsOnTLSMaxVersionBelowFloor(t *testing.T) {
	assert.Panics(t, func() {
		newRedisModule(&goredis.Options{
			Addr:      "localhost:6379",
			TLSConfig: &tls.Config{MaxVersion: tls.VersionTLS11},
		}, true)
	})
}

func TestNewRedisModule_PanicsOnNonLoopbackWithoutTLS(t *testing.T) {
	assert.Panics(t, func() {
		newRedisModule(&goredis.Options{Addr: "redis.example.com:6379"}, false)
	})
}

func TestNewRedisModule_PanicsOnNonLoopbackWithoutPassword(t *testing.T) {
	assert.Panics(t, func() {
		newRedisModule(&goredis.Options{
			Addr:      "redis.example.com:6379",
			TLSConfig: &tls.Config{},
		}, false)
	})
}

func TestNewRedisModule_AllowsNonLoopbackWithTLSAndPassword(t *testing.T) {
	assert.NotPanics(t, func() {
		newRedisModule(&goredis.Options{
			Addr:      "redis.example.com:6379",
			TLSConfig: &tls.Config{},
			Password:  "secret",
		}, false)
	})
}

func TestNewRedisModule_AllowsNonLoopbackWithRotatingCredentialProvider(t *testing.T) {
	assert.NotPanics(t, func() {
		newRedisModule(&goredis.Options{
			Addr:      "redis.example.com:6379",
			TLSConfig: &tls.Config{},
			CredentialsProviderContext: func(context.Context) (string, string, error) {
				return "user", "rotated", nil
			},
		}, false)
	})
}

func TestNewRedisModule_AllowsLoopbackWithoutTLS(t *testing.T) {
	assert.NotPanics(t, func() {
		newRedisModule(&goredis.Options{Addr: "localhost:6379"}, false)
	})
}

func TestNewRedisModule_AllowsPlaintextWithOptOut(t *testing.T) {
	assert.NotPanics(t, func() {
		newRedisModule(&goredis.Options{Addr: "redis.example.com:6379"}, true)
	})
}

func TestNewRedisModule_TransportSafety_Table(t *testing.T) {
	cases := []struct {
		name           string
		addr           string
		tlsConfig      *tls.Config
		password       string
		allowPlaintext bool
		wantPanic      bool
	}{
		{
			name: "non-loopback no TLS no password panics",
			addr: "redis.prod.example:6379", wantPanic: true,
		},
		{
			name:      "non-loopback TLS but no password panics",
			addr:      "redis.prod.example:6379",
			tlsConfig: &tls.Config{},
			wantPanic: true,
		},
		{
			name:      "non-loopback TLS and password allowed",
			addr:      "redis.prod.example:6379",
			tlsConfig: &tls.Config{},
			password:  "secret",
		},
		{
			name: "loopback name (localhost) plaintext allowed",
			addr: "localhost:6379",
		},
		{
			name: "loopback IPv4 (127.0.0.1) plaintext allowed",
			addr: "127.0.0.1:6379",
		},
		{
			name: "loopback IPv6 ([::1]) plaintext allowed",
			addr: "[::1]:6379",
		},
		{
			name:      "wildcard 0.0.0.0 is not loopback and panics",
			addr:      "0.0.0.0:6379",
			wantPanic: true,
		},
		{
			name:           "non-loopback plaintext allowed via WithoutRedisTLS opt-out",
			addr:           "redis.prod.example:6379",
			allowPlaintext: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts := &goredis.Options{Addr: tc.addr, TLSConfig: tc.tlsConfig, Password: tc.password}
			fn := func() { newRedisModule(opts, tc.allowPlaintext) }
			if tc.wantPanic {
				assert.Panics(t, fn)
			} else {
				assert.NotPanics(t, fn)
			}
		})
	}
}

func TestRedisModule_HealthChecksBeforeInit(t *testing.T) {
	m := newRedisModule(&goredis.Options{Addr: "localhost:6379"}, true)
	checks := m.HealthChecks()
	assert.Nil(t, checks, "should return nil health checks before Init")
}

func TestRedisModule_StopBeforeInit(t *testing.T) {
	m := newRedisModule(&goredis.Options{Addr: "localhost:6379"}, true)
	err := m.Stop(context.Background())
	require.NoError(t, err, "Stop before Init should not error")
}

func TestRedisModule_PopulateBeforeInit(t *testing.T) {
	m := newRedisModule(&goredis.Options{Addr: "localhost:6379"}, true)
	infra := &Infrastructure{}
	m.Populate(infra)
	assert.Nil(t, infra.Redis, "Redis should be nil before Init")
}

func TestRedisModule_WithModuleIntegration(t *testing.T) {
	b := New("test", "v1", BaseConfig{}).
		WithRedis(&goredis.Options{Addr: "localhost:6379"})
	assert.NotNil(t, b.redisOpts)
}

func TestBuildIntegrationModules_Redis(t *testing.T) {
	b := New("test", "v1", BaseConfig{}).
		WithRedis(&goredis.Options{Addr: "localhost:6379"})

	modules := b.buildIntegrationModules()
	assert.True(t, hasModule(modules, "redis"), "redis module should be present")
	assert.True(t, hasModule(modules, "httpclient"), "httpclient module should always be present")
}

func TestBuildIntegrationModules_NoRedis(t *testing.T) {
	b := New("test", "v1", BaseConfig{})
	modules := b.buildIntegrationModules()
	// httpclient is always present even without redis.
	assert.True(t, hasModule(modules, "httpclient"), "httpclient should always be present")
	assert.False(t, hasModule(modules, "redis"), "redis should not be present")
}
