package app

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// publicMiddlewareModule records whether the Builder asked it for public
// middleware, which a worker must never install.
type publicMiddlewareModule struct {
	BaseModule
	asked atomic.Bool
}

func (m *publicMiddlewareModule) PublicMiddleware() []PhasedMiddleware {
	m.asked.Store(true)
	return []PhasedMiddleware{{Phase: PhaseAuth, Func: func(next http.Handler) http.Handler { return next }}}
}

func TestValidate_WithoutPublicServer(t *testing.T) {
	t.Run("needs no TLS, rate limit or server port", func(t *testing.T) {
		b := New("worker", "v0.1.0", BaseConfig{Internal: InternalConfig{Port: 9090}}).WithoutPublicServer()
		require.NoError(t, b.Validate())
	})
	t.Run("still validates the internal port", func(t *testing.T) {
		b := New("worker", "v0.1.0", BaseConfig{}).WithoutPublicServer()
		require.Error(t, b.Validate())
	})
	t.Run("still enforces internal loopback", func(t *testing.T) {
		b := New("worker", "v0.1.0", BaseConfig{Internal: InternalConfig{Host: "0.0.0.0", Port: 9090}}).WithoutPublicServer()
		err := b.Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "Internal.Host is not loopback")
	})
	t.Run("rejects Router", func(t *testing.T) {
		b := New("worker", "v0.1.0", validBaseConfig()).WithoutPublicServer().
			Router(func(Infrastructure) http.Handler { return http.NotFoundHandler() })
		err := b.Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "Router is not allowed with WithoutPublicServer")
	})
	t.Run("rejects rate-limit modules", func(t *testing.T) {
		b := New("worker", "v0.1.0", validBaseConfig()).WithoutPublicServer().
			With(fakeRateLimitDeclarer{BaseModule: NewBaseModule("fake-rl")})
		err := b.Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "rate-limit modules are not allowed")
	})
	t.Run("default still requires public-server declarations", func(t *testing.T) {
		b := New("svc", "v0.1.0", validBaseConfig())
		err := b.Validate()
		require.Error(t, err)
		assert.True(t, strings.Contains(err.Error(), "TLS must be configured"))
	})
}

func TestBuilder_WithoutPublicServerRunsInternalServerOnly(t *testing.T) {
	srvPort := freePort(t)
	intPort := freePort(t)
	mw := &publicMiddlewareModule{BaseModule: NewBaseModule("public-mw")}
	var bgStarted atomic.Bool

	b := New("worker", "v0.0.1", BaseConfig{
		Server:   ServerConfig{Host: "127.0.0.1", Port: srvPort},
		Internal: InternalConfig{Host: "127.0.0.1", Port: intPort},
	}).
		WithoutPublicServer().
		With(mw).
		Background("consumer", func(ctx context.Context) error {
			bgStarted.Store(true)
			<-ctx.Done()
			return nil
		})

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- b.RunContext(ctx) }()

	waitForHTTP(t, fmt.Sprintf("http://127.0.0.1:%d/ready", intPort), 3*time.Second)
	assert.Eventually(t, bgStarted.Load, time.Second, 10*time.Millisecond)
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", srvPort), 200*time.Millisecond)
	if err == nil {
		_ = conn.Close()
		t.Fatal("public port must not be bound without a public server")
	}
	assert.False(t, mw.asked.Load(), "public middleware must not be installed")

	cancel()
	select {
	case runErr := <-errCh:
		require.NoError(t, runErr)
	case <-time.After(10 * time.Second):
		t.Fatal("RunContext did not return after cancellation")
	}
}
