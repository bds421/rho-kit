package app

import (
	"testing"

	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewRedisModule_PanicsOnNilOpts(t *testing.T) {
	defer func() {
		r := recover()
		require.NotNil(t, r, "expected panic for nil redis options")
		assert.Contains(t, r, "must not be nil")
	}()
	newRedisModule(nil)
}

func TestRedisModule_Name(t *testing.T) {
	m := newRedisModule(&goredis.Options{Addr: "localhost:6379"})
	assert.Equal(t, "redis", m.Name())
}

func TestRedisModule_HealthChecksBeforeInit(t *testing.T) {
	m := newRedisModule(&goredis.Options{Addr: "localhost:6379"})
	checks := m.HealthChecks()
	assert.Nil(t, checks, "should return nil health checks before Init")
}

func TestRedisModule_CloseBeforeInit(t *testing.T) {
	m := newRedisModule(&goredis.Options{Addr: "localhost:6379"})
	err := m.Close(nil)
	require.NoError(t, err, "Close before Init should not error")
}

func TestRedisModule_PopulateBeforeInit(t *testing.T) {
	m := newRedisModule(&goredis.Options{Addr: "localhost:6379"})
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
	require.Len(t, modules, 1)
	assert.Equal(t, "redis", modules[0].Name())
}

func TestBuildIntegrationModules_NoRedis(t *testing.T) {
	b := New("test", "v1", BaseConfig{})
	modules := b.buildIntegrationModules()
	assert.Empty(t, modules)
}
