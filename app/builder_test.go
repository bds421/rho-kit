package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/observability/v2/auditlog"
	"github.com/bds421/rho-kit/observability/v2/health"
)

type runContextValueKey struct{}

func TestBuilder_FluentChaining(t *testing.T) {
	b := New("test-svc", "v0.1.0", BaseConfig{}).
		WithoutRateLimit().
		EventBusPool(4).
		With(NewBaseModule("svc-extra")).
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
		EventBusPool(4)
	assert.Equal(t, 4, b.eventBusPoolSize)
}

func TestBuilder_WithEventBusPoolPanicsOnZero(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for zero pool size")
		}
	}()
	New("test-svc", "v0.1.0", BaseConfig{}).EventBusPool(0)
}

func TestBuilder_WithEventBusPoolPanicsOnNegative(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for negative pool size")
		}
	}()
	New("test-svc", "v0.1.0", BaseConfig{}).EventBusPool(-1)
}

func TestBuilder_WithAuditLogClonesOptions(t *testing.T) {
	opts := []auditlog.Option{auditlog.WithLogger(slog.Default())}

	b := New("test-svc", "v0.1.0", BaseConfig{}).AuditLog(auditlog.NewMemoryStore(), opts...)
	opts[0] = nil

	require.Len(t, b.auditOpts, 1)
	assert.NotNil(t, b.auditOpts[0])
}

// TestBuilder_WithCronClonesOptions was removed when cron moved to
// app/cron — option cloning is now an app/cron.Module concern,
// covered by its own tests.

func TestBuilder_WithLogger(t *testing.T) {
	b := New("test-svc", "v0.1.0", BaseConfig{}).
		Logger(nil)
	if b.logger != nil {
		t.Fatal("logger should be nil (falls back to slog.Default)")
	}
}

func TestBuilder_ServerErrorLogOptionUsesConfiguredLogger(t *testing.T) {
	var out bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&out, nil))
	srv := &http.Server{}

	serverErrorLogOption(logger)(srv)
	require.NotNil(t, srv.ErrorLog)

	srv.ErrorLog.Print("public server probe")
	assert.Contains(t, out.String(), "public server probe")
}

// Storage / NamedStorage panic-on-nil tests moved to app/storage —
// the functional options now own the construction-time contract.

// Rate-limit Builder-method-shaped tests have moved to
// app/ratelimit (IPModule / KeyedModule own the parameter checks
// and dedupe contract now).

// ServerOption / StackOptions / CustomReadiness nil-panic tests moved
// to app/http — the functional options now own the construction-time
// contract.

func TestBuilder_AddHealthCheckPanicsOnInvalidCheck(t *testing.T) {
	assert.Panics(t, func() {
		New("test-svc", "v0.1.0", BaseConfig{}).
			AddHealthCheck(health.DependencyCheck{Name: "Bad Name", Check: func(_ context.Context) string { return health.StatusHealthy }})
	})
	assert.Panics(t, func() {
		New("test-svc", "v0.1.0", BaseConfig{}).
			AddHealthCheck(health.DependencyCheck{Name: "bad-check"})
	})
}

func TestBuilder_WithBackgroundPanicsOnInvalidInput(t *testing.T) {
	assert.Panics(t, func() {
		New("test-svc", "v0.1.0", BaseConfig{}).
			Background("", func(_ context.Context) error { return nil })
	})
	assert.Panics(t, func() {
		New("test-svc", "v0.1.0", BaseConfig{}).Background("bg", nil)
	})
}

func TestBuilder_WithStartupTimeoutPanicsOnInvalidInput(t *testing.T) {
	assert.Panics(t, func() {
		New("test-svc", "v0.1.0", BaseConfig{}).StartupTimeout(0)
	})
	assert.Panics(t, func() {
		New("test-svc", "v0.1.0", BaseConfig{}).StartupTimeout(-time.Second)
	})
}

func TestBuilder_RouterPanicsOnNil(t *testing.T) {
	assert.Panics(t, func() {
		New("test-svc", "v0.1.0", BaseConfig{}).Router(nil)
	})
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

func TestBuilder_RunContextRejectsNilContext(t *testing.T) {
	b := New("test-svc", "v0.1.0", BaseConfig{})

	err := b.RunContext(nil) //nolint:staticcheck // exercises explicit nil-context guard

	require.Error(t, err)
	assert.Contains(t, err.Error(), "RunContext requires a non-nil context")
	assert.False(t, b.ran, "nil context should not consume the one-shot builder")
}

func TestBuilder_RunContextReturnsCanceledBeforeStartup(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	b := New("test-svc", "v0.1.0", BaseConfig{})

	err := b.RunContext(ctx)

	require.ErrorIs(t, err, context.Canceled)
	assert.False(t, b.ran, "pre-canceled context should not consume the one-shot builder")
}

func TestTestInfrastructure(t *testing.T) {
	infra := TestInfrastructure()
	if infra.Logger == nil {
		t.Fatal("Logger should not be nil")
	}
	if infra.HTTPClient == nil {
		t.Fatal("HTTPClient should not be nil")
	}
	if infra.HTTPClient.Timeout <= 0 {
		t.Fatal("HTTPClient should have a bounded timeout")
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
			With(allowPlaintextOnly()).
			WithoutRateLimit().
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
			With(allowPlaintextOnly()).
			WithoutRateLimit().
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

	t.Run("RunContextCancellation", func(t *testing.T) {
		srvPort := freePort(t)
		intPort := freePort(t)

		cfg := BaseConfig{
			Server:   ServerConfig{Host: "127.0.0.1", Port: srvPort},
			Internal: InternalConfig{Host: "127.0.0.1", Port: intPort},
		}

		var bgStarted atomic.Bool
		var bgStopped atomic.Bool
		var moduleCloseValue any
		var moduleCloseErr error
		module := newStubModule("ctx-value-module")
		module.stopFn = func(closeCtx context.Context) error {
			moduleCloseValue = closeCtx.Value(runContextValueKey{})
			moduleCloseErr = closeCtx.Err()
			return nil
		}

		b := New("lifecycle-context-test", "v0.0.3", cfg).
			With(allowPlaintextOnly()).
			WithoutRateLimit().
			With(module).
			Background("ctx-bg", func(ctx context.Context) error {
				bgStarted.Store(true)
				<-ctx.Done()
				bgStopped.Store(true)
				return nil
			}).
			Router(func(infra Infrastructure) http.Handler {
				return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(http.StatusNoContent)
				})
			})

		parent := context.WithValue(context.Background(), runContextValueKey{}, "trace-123")
		ctx, cancel := context.WithCancel(parent)
		errCh := make(chan error, 1)
		go func() { errCh <- b.RunContext(ctx) }()

		srvURL := fmt.Sprintf("http://127.0.0.1:%d", srvPort)
		intURL := fmt.Sprintf("http://127.0.0.1:%d", intPort)
		waitForHTTP(t, srvURL, 3*time.Second)
		waitForHTTP(t, intURL+"/ready", 3*time.Second)
		assert.Eventually(t, bgStarted.Load, time.Second, 10*time.Millisecond)

		cancel()
		select {
		case runErr := <-errCh:
			require.NoError(t, runErr)
		case <-time.After(10 * time.Second):
			t.Fatal("RunContext did not return within 10s after context cancellation")
		}
		assert.True(t, bgStopped.Load(), "background should observe RunContext cancellation")
		assert.Equal(t, "trace-123", moduleCloseValue, "module cleanup should preserve parent context values")
		assert.NoError(t, moduleCloseErr, "module cleanup should not inherit parent cancellation")
	})
}

func TestRunShutdownHooks_RecoversPanicInHookGoroutine(t *testing.T) {
	var secondCalled atomic.Bool
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	assert.NotPanics(t, func() {
		runShutdownHooks(context.Background(), []func(context.Context){
			func(context.Context) {
				panic("hook exploded")
			},
			func(context.Context) {
				secondCalled.Store(true)
			},
		}, logger)
	})
	assert.True(t, secondCalled.Load(), "a panicking hook must not prevent later hooks")
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
