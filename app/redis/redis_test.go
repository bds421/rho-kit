package redis

import (
	"context"
	"crypto/tls"
	"testing"

	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/app/v2"
	kitredis "github.com/bds421/rho-kit/infra/redis/v2"
)

func TestModule_PanicsOnNilOpts(t *testing.T) {
	defer func() {
		r := recover()
		require.NotNil(t, r, "expected panic for nil redis options")
		assert.Contains(t, r, "non-nil options")
	}()
	_ = Module(nil)
}

func TestModule_Name(t *testing.T) {
	m := Module(&goredis.Options{Addr: "localhost:6379"}, WithoutTLS())
	assert.Equal(t, "redis", m.Name())
}

func TestModule_ClonesOptionsAndConnOptions(t *testing.T) {
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS10, NextProtos: []string{"h2"}, ServerName: "before.example"}
	opts := &goredis.Options{Addr: "localhost:6379", TLSConfig: tlsConfig}
	connOpts := []kitredis.ConnOption{kitredis.WithInstance("primary")}

	m := Module(opts, WithConn(connOpts...)).(*redisModule)
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

func TestModule_PanicsOnTLSMaxVersionBelowFloor(t *testing.T) {
	assert.Panics(t, func() {
		_ = Module(&goredis.Options{
			Addr:      "localhost:6379",
			TLSConfig: &tls.Config{MaxVersion: tls.VersionTLS11},
		})
	})
}

func TestModule_PanicsOnNonLoopbackWithoutTLS(t *testing.T) {
	assert.Panics(t, func() {
		_ = Module(&goredis.Options{Addr: "redis.example.com:6379"})
	})
}

func TestModule_PanicsOnNonLoopbackWithoutPassword(t *testing.T) {
	assert.Panics(t, func() {
		_ = Module(&goredis.Options{
			Addr:      "redis.example.com:6379",
			TLSConfig: &tls.Config{},
		})
	})
}

func TestModule_AllowsNonLoopbackWithTLSAndPassword(t *testing.T) {
	assert.NotPanics(t, func() {
		_ = Module(&goredis.Options{
			Addr:      "redis.example.com:6379",
			TLSConfig: &tls.Config{},
			Password:  "secret",
		})
	})
}

func TestModule_AllowsNonLoopbackWithRotatingCredentialProvider(t *testing.T) {
	assert.NotPanics(t, func() {
		_ = Module(&goredis.Options{
			Addr:      "redis.example.com:6379",
			TLSConfig: &tls.Config{},
			CredentialsProviderContext: func(context.Context) (string, string, error) {
				return "user", "rotated", nil
			},
		})
	})
}

func TestModule_AllowsLoopbackWithoutTLS(t *testing.T) {
	assert.NotPanics(t, func() {
		_ = Module(&goredis.Options{Addr: "localhost:6379"})
	})
}

func TestModule_AllowsPlaintextWithOptOut(t *testing.T) {
	assert.NotPanics(t, func() {
		_ = Module(&goredis.Options{Addr: "redis.example.com:6379"}, WithoutTLS())
	})
}

func TestModule_TransportSafety_Table(t *testing.T) {
	cases := []struct {
		name           string
		addr           string
		tlsConfig      *tls.Config
		password       string
		allowPlaintext bool
		wantPanic      bool
	}{
		{name: "non-loopback no TLS no password panics", addr: "redis.prod.example:6379", wantPanic: true},
		{name: "non-loopback TLS but no password panics", addr: "redis.prod.example:6379", tlsConfig: &tls.Config{}, wantPanic: true},
		{name: "non-loopback TLS and password allowed", addr: "redis.prod.example:6379", tlsConfig: &tls.Config{}, password: "secret"},
		{name: "loopback name plaintext allowed", addr: "localhost:6379"},
		{name: "loopback IPv4 plaintext allowed", addr: "127.0.0.1:6379"},
		{name: "loopback IPv6 plaintext allowed", addr: "[::1]:6379"},
		{name: "wildcard 0.0.0.0 not loopback and panics", addr: "0.0.0.0:6379", wantPanic: true},
		{name: "non-loopback plaintext allowed via WithoutTLS", addr: "redis.prod.example:6379", allowPlaintext: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts := &goredis.Options{Addr: tc.addr, TLSConfig: tc.tlsConfig, Password: tc.password}
			fn := func() {
				if tc.allowPlaintext {
					_ = Module(opts, WithoutTLS())
				} else {
					_ = Module(opts)
				}
			}
			if tc.wantPanic {
				assert.Panics(t, fn)
			} else {
				assert.NotPanics(t, fn)
			}
		})
	}
}

func TestModule_HealthChecksBeforeInit(t *testing.T) {
	m := Module(&goredis.Options{Addr: "localhost:6379"}).(*redisModule)
	assert.Nil(t, m.HealthChecks())
}

func TestModule_StopBeforeInit(t *testing.T) {
	m := Module(&goredis.Options{Addr: "localhost:6379"}).(*redisModule)
	require.NoError(t, m.Stop(context.Background()))
}

func TestConnection_NilWhenAdapterNotRegistered(t *testing.T) {
	infra := app.TestInfrastructure()
	assert.Nil(t, Connection(infra))
}
