package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"google.golang.org/grpc"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/infra/sqldb"
	"github.com/bds421/rho-kit/infra/storage/membackend"
	"github.com/bds421/rho-kit/observability/health"
	"github.com/bds421/rho-kit/observability/tracing"
)

func TestBuilder_FluentChaining(t *testing.T) {
	b := New("test-svc", "v0.1.0", BaseConfig{}).
		WithIPRateLimit(100, time.Minute).
		WithKeyedRateLimit("api", 50, time.Minute).
		WithStorage(membackend.New()).
		WithNamedStorage("local", membackend.New()).
		WithTracing(tracing.Config{}).
		WithCron().
		WithEventBusPool(4).
		WithModule(NewGRPCModule(func(_ *grpc.Server) {}, ":50051")).
		WithServerOption(WithWriteTimeout(0)).
		AddHealthCheck(health.DependencyCheck{Name: "test", Check: func(_ context.Context) string { return "healthy" }}).
		Background("bg", func(_ context.Context) error { return nil }).
		OnShutdown(func(_ context.Context) {}).
		Router(func(infra Infrastructure) http.Handler { return http.NotFoundHandler() })

	if b == nil {
		t.Fatal("builder chain returned nil")
	}
}

func TestBuilder_WithEventBusPool(t *testing.T) {
	b := New("test-svc", "v0.1.0", BaseConfig{}).
		WithEventBusPool(4)
	assert.Equal(t, 4, b.eventBusPoolSize)
}

func TestBuilder_WithEventBusPoolPanicsOnZero(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for zero pool size")
		}
	}()
	New("test-svc", "v0.1.0", BaseConfig{}).WithEventBusPool(0)
}

func TestBuilder_WithEventBusPoolPanicsOnNegative(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for negative pool size")
		}
	}()
	New("test-svc", "v0.1.0", BaseConfig{}).WithEventBusPool(-1)
}

func TestBuilder_WithRedis(t *testing.T) {
	b := New("test-svc", "v0.1.0", BaseConfig{}).
		WithRedis(&goredis.Options{Addr: "localhost:6379"})

	if b.redisOpts == nil {
		t.Fatal("redisOpts should be set")
	}
}

func TestBuilder_WithRedisPanicsOnNil(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for nil redis options")
		}
	}()
	New("test-svc", "v0.1.0", BaseConfig{}).WithRedis(nil)
}

func TestBuilder_WithLogger(t *testing.T) {
	b := New("test-svc", "v0.1.0", BaseConfig{}).
		WithLogger(nil)
	if b.logger != nil {
		t.Fatal("logger should be nil (falls back to slog.Default)")
	}
}

func TestBuilder_WithStoragePanicsOnNil(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for nil storage")
		}
	}()
	New("test-svc", "v0.1.0", BaseConfig{}).WithStorage(nil)
}

func TestBuilder_WithNamedStoragePanicsOnEmpty(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for empty storage name")
		}
	}()
	New("test-svc", "v0.1.0", BaseConfig{}).WithNamedStorage("", membackend.New())
}

func TestBuilder_WithNamedStoragePanicsOnNil(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for nil named storage")
		}
	}()
	New("test-svc", "v0.1.0", BaseConfig{}).WithNamedStorage("s3", nil)
}

func TestBuilder_DuplicateKeyedRateLimiterPanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for duplicate keyed rate limiter")
		}
	}()
	New("test-svc", "v0.1.0", BaseConfig{}).
		WithKeyedRateLimit("api", 10, time.Second).
		WithKeyedRateLimit("api", 20, time.Second)
}

func TestBuilder_RunPanicsOnReuse(t *testing.T) {
	b := New("test-svc", "v0.1.0", BaseConfig{})
	b.ran = true
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for double Run")
		}
	}()
	_ = b.Run()
}

func TestBuilder_WithMySQL(t *testing.T) {
	b := New("test-svc", "v0.1.0", BaseConfig{}).
		WithMySQL(sqldb.MySQLConfig{}, sqldb.PoolConfig{})
	if b.dbMySQLCfg == nil {
		t.Fatal("MySQL config should be set")
	}
	if b.dbPgCfg != nil {
		t.Fatal("PostgreSQL config should be nil after WithMySQL")
	}
}

func TestBuilder_WithPostgres(t *testing.T) {
	b := New("test-svc", "v0.1.0", BaseConfig{}).
		WithPostgres(sqldb.PostgresConfig{}, sqldb.PoolConfig{})
	if b.dbPgCfg == nil {
		t.Fatal("PostgreSQL config should be set")
	}
	if b.dbMySQLCfg != nil {
		t.Fatal("MySQL config should be nil after WithPostgres")
	}
}

func TestTestInfrastructure(t *testing.T) {
	infra := TestInfrastructure()
	if infra.Logger == nil {
		t.Fatal("Logger should not be nil")
	}
	if infra.HTTPClient == nil {
		t.Fatal("HTTPClient should not be nil")
	}
	// Function fields should be callable without panicking.
	infra.Background("test", func(_ context.Context) error { return nil })
	infra.SetCustomReadiness(http.NotFoundHandler())
	infra.AddHealthCheck(health.DependencyCheck{Name: "test"})
}

// WriteTimeout is re-exported here for testing the fluent chain without importing httpx.
func WithWriteTimeout(d time.Duration) func(s *http.Server) {
	return func(s *http.Server) { s.WriteTimeout = d }
}

// freePort returns an available TCP port on localhost.
func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := l.Addr().(*net.TCPAddr).Port
	require.NoError(t, l.Close())
	return port
}

// waitForHTTP polls url until it responds with a 2xx or the timeout expires.
func waitForHTTP(t *testing.T, url string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := http.Get(url)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("server at %s not ready within %v", url, timeout)
}

// TestBuilder_Lifecycle tests the full start/serve/shutdown lifecycle.
// These tests use SIGINT to trigger shutdown, which is process-global.
// Only one lifecycle test can run at a time; they are combined into a
// single test to avoid signal interference between parallel subtests.
func TestBuilder_Lifecycle(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}

	t.Run("StartServeShutdownWithHealthChecks", func(t *testing.T) {
		srvPort := freePort(t)
		intPort := freePort(t)

		cfg := BaseConfig{
			Server:   ServerConfig{Host: "127.0.0.1", Port: srvPort},
			Internal: InternalConfig{Host: "127.0.0.1", Port: intPort},
		}

		var routerCalled atomic.Bool

		b := New("lifecycle-test", "v0.0.1", cfg).
			AddHealthCheck(health.DependencyCheck{
				Name:     "test-dep",
				Check:    func(_ context.Context) string { return health.StatusHealthy },
				Critical: true,
			}).
			AddHealthCheck(health.DependencyCheck{
				Name:     "non-critical",
				Check:    func(_ context.Context) string { return health.StatusDegraded },
				Critical: false,
			}).
			Router(func(infra Infrastructure) http.Handler {
				routerCalled.Store(true)
				infra.AddHealthCheck(health.DependencyCheck{
					Name:  "late-check",
					Check: func(_ context.Context) string { return health.StatusHealthy },
				})
				return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write([]byte(`{"ok":true}`))
				})
			})

		errCh := make(chan error, 1)
		go func() { errCh <- b.Run() }()

		srvURL := fmt.Sprintf("http://127.0.0.1:%d", srvPort)
		intURL := fmt.Sprintf("http://127.0.0.1:%d", intPort)
		waitForHTTP(t, srvURL, 3*time.Second)
		waitForHTTP(t, intURL+"/ready", 3*time.Second)

		assert.True(t, routerCalled.Load(), "RouterFunc should have been called")

		// Verify the public server serves requests.
		resp, err := http.Get(srvURL)
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()
		assert.Equal(t, http.StatusOK, resp.StatusCode)

		// Verify health checks (including late-registered checks).
		resp2, err := http.Get(intURL + "/ready")
		require.NoError(t, err)
		body, _ := io.ReadAll(resp2.Body)
		_ = resp2.Body.Close()
		assert.Equal(t, http.StatusOK, resp2.StatusCode)
		var healthResp health.Response
		require.NoError(t, json.Unmarshal(body, &healthResp))
		assert.Equal(t, "degraded", healthResp.Status) // non-critical degraded
		assert.Equal(t, "v0.0.1", healthResp.Version)
		assert.Equal(t, "healthy", healthResp.Services["test-dep"])
		assert.Equal(t, "degraded", healthResp.Services["non-critical"])
		assert.Equal(t, "healthy", healthResp.Services["late-check"])

		// Graceful shutdown via SIGINT.
		require.NoError(t, syscall.Kill(syscall.Getpid(), syscall.SIGINT))

		select {
		case runErr := <-errCh:
			if runErr != nil && runErr.Error() != "context canceled" {
				t.Fatalf("unexpected Run error: %v", runErr)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("Run did not return within 10s after SIGINT")
		}
		// Drain pending signals before next test.
		drainSignals()
	})

	t.Run("BackgroundAndShutdownHook", func(t *testing.T) {
		srvPort := freePort(t)
		intPort := freePort(t)

		cfg := BaseConfig{
			Server:   ServerConfig{Host: "127.0.0.1", Port: srvPort},
			Internal: InternalConfig{Host: "127.0.0.1", Port: intPort},
		}

		var bgStarted atomic.Bool
		var bgStopped atomic.Bool
		var hookCalled atomic.Bool
		var lateBgStarted atomic.Bool

		b := New("lifecycle-bg-test", "v0.0.2", cfg).
			Background("early-bg", func(ctx context.Context) error {
				bgStarted.Store(true)
				<-ctx.Done()
				bgStopped.Store(true)
				return nil
			}).
			OnShutdown(func(ctx context.Context) {
				hookCalled.Store(true)
			}).
			Router(func(infra Infrastructure) http.Handler {
				infra.Background("late-bg", func(ctx context.Context) error {
					lateBgStarted.Store(true)
					<-ctx.Done()
					return nil
				})
				return http.NotFoundHandler()
			})

		errCh := make(chan error, 1)
		go func() { errCh <- b.Run() }()

		intURL := fmt.Sprintf("http://127.0.0.1:%d", intPort)
		waitForHTTP(t, intURL+"/ready", 3*time.Second)

		time.Sleep(100 * time.Millisecond)
		assert.True(t, bgStarted.Load(), "early background should have started")
		assert.True(t, lateBgStarted.Load(), "late background should have started")

		require.NoError(t, syscall.Kill(syscall.Getpid(), syscall.SIGINT))

		select {
		case <-errCh:
		case <-time.After(10 * time.Second):
			t.Fatal("Run did not return within 10s after SIGINT")
		}

		assert.True(t, bgStopped.Load(), "early background should have stopped")
		assert.True(t, hookCalled.Load(), "shutdown hook should have been called")
		// Drain any pending SIGINT delivery before allowing next test to start.
		drainSignals()
	})
}

// drainSignals absorbs any pending SIGINT/SIGTERM signals from a previous
// test's shutdown trigger. Without this, the signal can be delivered to the
// next test's process, killing it before its handler is registered.
func drainSignals() {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
	// Give the OS time to deliver any pending signal.
	timer := time.NewTimer(200 * time.Millisecond)
	select {
	case <-ch:
	case <-timer.C:
	}
	signal.Stop(ch)
}
