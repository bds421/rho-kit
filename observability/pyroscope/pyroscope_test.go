package pyroscope_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/observability/pyroscope/v2"
)

func TestComponent_RequiresServerAddress(t *testing.T) {
	_, err := pyroscope.Component(pyroscope.Config{AppName: "x"})
	require.Error(t, err)
}

func TestComponent_RequiresAppName(t *testing.T) {
	_, err := pyroscope.Component(pyroscope.Config{ServerAddress: "http://x"})
	require.Error(t, err)
}

func TestComponent_PanicsOnNilOption(t *testing.T) {
	_, err := pyroscope.Component(pyroscope.Config{
		ServerAddress: "http://x",
		AppName:       "x",
	}, nil)
	require.Error(t, err)
}

func TestComponent_DefaultsApplied(t *testing.T) {
	cmp, err := pyroscope.Component(pyroscope.Config{
		ServerAddress: "http://localhost:1",
		AppName:       "test-svc",
	})
	require.NoError(t, err)
	require.NotNil(t, cmp)
}

// TestProfiler_StartStop verifies the lifecycle round-trip against a
// throwaway HTTP server impersonating Pyroscope. We don't validate the
// upload bodies — the goal is to confirm Start/Stop wire pyroscope-go
// correctly and that Stop is idempotent.
func TestProfiler_StartStop(t *testing.T) {
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer fake.Close()

	cmp, err := pyroscope.Component(pyroscope.Config{
		ServerAddress: fake.URL,
		AppName:       "test-svc",
		UploadRate:    200 * time.Millisecond,
	})
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	startDone := make(chan error, 1)
	go func() {
		startDone <- cmp.Start(ctx)
	}()

	// Give the profiler a beat to install its sampler.
	time.Sleep(50 * time.Millisecond)

	// Cancel ctx → Start returns nil per Component contract.
	cancel()
	select {
	case err := <-startDone:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatalf("Start did not return after ctx cancel")
	}

	// Stop is idempotent.
	require.NoError(t, cmp.Stop(context.Background()))
	require.NoError(t, cmp.Stop(context.Background()))
}

func TestProfiler_DoubleStartErrors(t *testing.T) {
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer fake.Close()

	cmp, err := pyroscope.Component(pyroscope.Config{
		ServerAddress: fake.URL,
		AppName:       "test-svc-double",
	})
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	go cmp.Start(ctx) //nolint:errcheck // expected to return on cancel
	time.Sleep(50 * time.Millisecond)

	err = cmp.Start(context.Background())
	require.Error(t, err)

	cancel()
	_ = cmp.Stop(context.Background())
}
