package sqldb

import (
	"context"
	"errors"
	"testing"

	"github.com/bds421/rho-kit/observability/v2/health"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubPinger satisfies the legacy [Pinger] interface only.
type stubPinger struct {
	err error
}

func (p stubPinger) Ping() error { return p.err }

// stubContextPinger satisfies both [Pinger] and [ContextPinger]. It records
// the context it received via PingContext so tests can assert the framework's
// cancellation context is threaded through rather than discarded.
type stubContextPinger struct {
	err error

	gotCtx       context.Context
	pingCalled   bool
	pingCtxCalls int
}

func (p *stubContextPinger) Ping() error { p.pingCalled = true; return p.err }

func (p *stubContextPinger) PingContext(ctx context.Context) error {
	p.pingCtxCalls++
	p.gotCtx = ctx
	return p.err
}

func TestHealthCheck_PanicsOnNilPinger(t *testing.T) {
	assert.Panics(t, func() {
		HealthCheck(nil)
	})
}

func TestHealthCheck_LegacyPingerStatus(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{name: "healthy", err: nil, want: health.StatusHealthy},
		{name: "unhealthy", err: errors.New("boom"), want: health.StatusUnhealthy},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			check := HealthCheck(stubPinger{err: tt.err})
			assert.Equal(t, "database", check.Name)
			assert.True(t, check.Critical)
			require.NotNil(t, check.Check)
			assert.Equal(t, tt.want, check.Check(context.Background()))
		})
	}
}

// TestHealthCheck_ThreadsContextWhenAvailable verifies that when the supplied
// pinger also implements [ContextPinger], HealthCheck threads the framework's
// cooperative-cancellation context through PingContext instead of calling the
// context-less Ping. A discarded context lets a hung ping keep holding a DB
// connection after the kubelet probe has given up — the resource leak this
// closure must avoid.
func TestHealthCheck_ThreadsContextWhenAvailable(t *testing.T) {
	p := &stubContextPinger{}
	check := HealthCheck(p)

	type ctxKey struct{}
	ctx := context.WithValue(context.Background(), ctxKey{}, "framework")

	status := check.Check(ctx)

	assert.Equal(t, health.StatusHealthy, status)
	assert.Equal(t, 1, p.pingCtxCalls, "PingContext must be used for a ContextPinger")
	assert.False(t, p.pingCalled, "context-less Ping must not be called when PingContext is available")
	require.NotNil(t, p.gotCtx)
	assert.Equal(t, "framework", p.gotCtx.Value(ctxKey{}), "the framework context must be threaded, not discarded")
}

func TestHealthCheckContext_PanicsOnNilPinger(t *testing.T) {
	assert.Panics(t, func() {
		HealthCheckContext(nil)
	})
}

func TestHealthCheckContext_ThreadsContext(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{name: "healthy", err: nil, want: health.StatusHealthy},
		{name: "unhealthy", err: errors.New("boom"), want: health.StatusUnhealthy},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &stubContextPinger{err: tt.err}
			check := HealthCheckContext(p)
			assert.Equal(t, "database", check.Name)
			assert.True(t, check.Critical)
			require.NotNil(t, check.Check)

			type ctxKey struct{}
			ctx := context.WithValue(context.Background(), ctxKey{}, "framework")

			assert.Equal(t, tt.want, check.Check(ctx))
			assert.Equal(t, 1, p.pingCtxCalls)
			require.NotNil(t, p.gotCtx)
			assert.Equal(t, "framework", p.gotCtx.Value(ctxKey{}))
		})
	}
}
