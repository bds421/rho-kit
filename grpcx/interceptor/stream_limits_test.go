package interceptor_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/bds421/rho-kit/grpcx/v2/interceptor"
)

// fakeServerStream is a minimal grpc.ServerStream stand-in for unit
// tests. The interceptor under test only reads Context, SendMsg, and
// RecvMsg; the rest delegate to safe defaults.
type fakeServerStream struct {
	ctx          context.Context
	sendMsgCount atomic.Int32
	recvMsgCount atomic.Int32
	recvBlockCh  chan struct{} // when set, RecvMsg blocks on this channel until close
}

func (s *fakeServerStream) Context() context.Context     { return s.ctx }
func (s *fakeServerStream) SetHeader(metadata.MD) error  { return nil }
func (s *fakeServerStream) SendHeader(metadata.MD) error { return nil }
func (s *fakeServerStream) SetTrailer(metadata.MD)       {}

func (s *fakeServerStream) SendMsg(_ any) error {
	s.sendMsgCount.Add(1)
	return nil
}

func (s *fakeServerStream) RecvMsg(_ any) error {
	s.recvMsgCount.Add(1)
	if s.recvBlockCh != nil {
		<-s.recvBlockCh
	}
	return nil
}

func TestMaxConcurrentStreamsServer_PanicsOnNonPositive(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on max<=0")
		}
	}()
	_ = interceptor.MaxConcurrentStreamsServer(0, nil)
}

func TestMaxConcurrentStreamsServer_AcceptsUpToMax(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := interceptor.NewStreamLimitMetrics(interceptor.WithRegisterer(reg))
	interc := interceptor.MaxConcurrentStreamsServer(2, m)

	release := make(chan struct{})
	handler := func(_ any, _ grpc.ServerStream) error {
		<-release
		return nil
	}

	info := &grpc.StreamServerInfo{FullMethod: "/test.Service/Stream"}

	type result struct {
		err error
	}
	r1 := make(chan result, 1)
	r2 := make(chan result, 1)
	r3 := make(chan result, 1)

	go func() { r1 <- result{interc(nil, &fakeServerStream{ctx: context.Background()}, info, handler)} }()
	go func() { r2 <- result{interc(nil, &fakeServerStream{ctx: context.Background()}, info, handler)} }()

	// Give the two acceptable streams a moment to enter handler.
	time.Sleep(100 * time.Millisecond)

	// Third stream is over cap → ResourceExhausted, handler never runs.
	go func() { r3 <- result{interc(nil, &fakeServerStream{ctx: context.Background()}, info, handler)} }()
	select {
	case res := <-r3:
		require.Error(t, res.err)
		st, ok := status.FromError(res.err)
		require.True(t, ok)
		assert.Equal(t, codes.ResourceExhausted, st.Code())
	case <-time.After(time.Second):
		t.Fatal("over-cap stream did not return promptly")
	}

	// Release the first two; they must complete.
	close(release)
	for _, ch := range []chan result{r1, r2} {
		select {
		case res := <-ch:
			require.NoError(t, res.err)
		case <-time.After(time.Second):
			t.Fatal("accepted stream did not finish")
		}
	}

	// Slot must now be free.
	r4 := make(chan result, 1)
	releasedHandler := func(_ any, _ grpc.ServerStream) error { return nil }
	go func() { r4 <- result{interc(nil, &fakeServerStream{ctx: context.Background()}, info, releasedHandler)} }()
	select {
	case res := <-r4:
		require.NoError(t, res.err, "slot must free up after streams complete")
	case <-time.After(time.Second):
		t.Fatal("post-release stream did not run")
	}
}

func TestStreamIdleTimeout_PanicsOnNonPositive(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on d<=0")
		}
	}()
	_ = interceptor.StreamIdleTimeout(0, nil)
}

func TestStreamIdleTimeout_CancelsIdleStream(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := interceptor.NewStreamLimitMetrics(interceptor.WithRegisterer(reg))
	interc := interceptor.StreamIdleTimeout(100*time.Millisecond, m)

	// Handler that just waits for ctx cancellation, mimicking an
	// idle handler that's blocked waiting for the next message.
	handler := func(_ any, ss grpc.ServerStream) error {
		<-ss.Context().Done()
		return ss.Context().Err()
	}

	info := &grpc.StreamServerInfo{FullMethod: "/test.Service/Stream"}
	ss := &fakeServerStream{ctx: context.Background()}

	err := interc(nil, ss, info, handler)
	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.DeadlineExceeded, st.Code(),
		"idle timeout should surface as DeadlineExceeded for client visibility")
}

func TestStreamIdleTimeout_ActivityResetsTimer(t *testing.T) {
	interc := interceptor.StreamIdleTimeout(150*time.Millisecond, nil)

	// Handler that sends/receives periodically — activity should
	// keep the stream alive past the idle timeout.
	handler := func(_ any, ss grpc.ServerStream) error {
		ticker := time.NewTicker(50 * time.Millisecond)
		defer ticker.Stop()
		ticks := 0
		for {
			select {
			case <-ss.Context().Done():
				return ss.Context().Err()
			case <-ticker.C:
				ticks++
				_ = ss.SendMsg("ping") // reset activity timer
				if ticks >= 5 {
					return nil // 250ms of activity > 150ms idle threshold
				}
			}
		}
	}

	info := &grpc.StreamServerInfo{FullMethod: "/test.Service/Stream"}
	ss := &fakeServerStream{ctx: context.Background()}

	err := interc(nil, ss, info, handler)
	require.NoError(t, err, "active stream must not be reaped by idle watchdog")
	assert.GreaterOrEqual(t, ss.sendMsgCount.Load(), int32(5))
}

func TestStreamIdleTimeout_HandlerCleanReturnSurvives(t *testing.T) {
	interc := interceptor.StreamIdleTimeout(200*time.Millisecond, nil)

	handler := func(_ any, _ grpc.ServerStream) error {
		// Handler returns immediately — no idle window opens.
		return nil
	}

	info := &grpc.StreamServerInfo{FullMethod: "/test.Service/Stream"}
	err := interc(nil, &fakeServerStream{ctx: context.Background()}, info, handler)
	require.NoError(t, err)
}

func TestStreamIdleTimeout_HandlerErrorPropagated(t *testing.T) {
	interc := interceptor.StreamIdleTimeout(200*time.Millisecond, nil)

	want := errors.New("application-specific failure")
	handler := func(_ any, _ grpc.ServerStream) error {
		return want
	}

	info := &grpc.StreamServerInfo{FullMethod: "/test.Service/Stream"}
	err := interc(nil, &fakeServerStream{ctx: context.Background()}, info, handler)
	require.ErrorIs(t, err, want, "handler errors must propagate unchanged when no idle timeout fired")
}

func TestNewStreamLimitMetrics_RegistersAllCollectors(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := interceptor.NewStreamLimitMetrics(interceptor.WithRegisterer(reg))
	require.NotNil(t, m)

	// Force at least one observation in each metric so Gather sees
	// the families.
	interc := interceptor.MaxConcurrentStreamsServer(1, m)
	release := make(chan struct{})
	defer close(release)
	go func() {
		_ = interc(nil, &fakeServerStream{ctx: context.Background()},
			&grpc.StreamServerInfo{FullMethod: "/test/Stream"},
			func(_ any, _ grpc.ServerStream) error {
				<-release
				return nil
			},
		)
	}()
	time.Sleep(50 * time.Millisecond)

	// Trigger a rejection so the rejected counter has a value.
	_ = interc(nil, &fakeServerStream{ctx: context.Background()},
		&grpc.StreamServerInfo{FullMethod: "/test/Stream"},
		func(_ any, _ grpc.ServerStream) error { return nil },
	)

	families, err := reg.Gather()
	require.NoError(t, err)

	names := map[string]bool{}
	for _, f := range families {
		names[f.GetName()] = true
	}
	assert.True(t, names["grpc_server_active_streams"])
	assert.True(t, names["grpc_server_streams_rejected_total"])
}
