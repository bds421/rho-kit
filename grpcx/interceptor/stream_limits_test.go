package interceptor_test

import (
	"context"
	"errors"
	"runtime"
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
	entered := make(chan struct{}, 2)
	handler := func(_ any, _ grpc.ServerStream) error {
		entered <- struct{}{}
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

	// Wait until both acceptable streams hold their slots before trying the
	// over-cap stream. This is deterministic and avoids scheduler sleeps.
	for range 2 {
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Fatal("accepted stream did not enter handler")
		}
	}

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
	interc := interceptor.StreamIdleTimeout(20*time.Millisecond, m)

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
	interc := interceptor.StreamIdleTimeout(100*time.Millisecond, nil)

	// Handler that sends/receives periodically — activity should
	// keep the stream alive past the idle timeout.
	handler := func(_ any, ss grpc.ServerStream) error {
		ticker := time.NewTicker(25 * time.Millisecond)
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
					return nil // 125ms of activity > 100ms idle threshold
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

// TestStreamIdleTimeout_WatchdogFireDoesNotMaskCleanReturn covers the
// case where the watchdog cancels the derived context (because the
// handler stopped sending/receiving) but the handler ignores ctx and
// still completes successfully — e.g. a client-streaming RPC that
// already replied via SendAndClose. The interceptor must surface the
// handler's real result, not an unconditional DeadlineExceeded.
func TestStreamIdleTimeout_WatchdogFireDoesNotMaskCleanReturn(t *testing.T) {
	interc := interceptor.StreamIdleTimeout(20*time.Millisecond, nil)

	// Waiting for cancellation proves the watchdog fired. Returning nil then
	// proves that cancellation is not substituted for the handler result.
	handler := func(_ any, ss grpc.ServerStream) error {
		<-ss.Context().Done()
		return nil
	}

	info := &grpc.StreamServerInfo{FullMethod: "/test.Service/Stream"}
	ss := &fakeServerStream{ctx: context.Background()}

	err := interc(nil, ss, info, handler)
	require.NoError(t, err,
		"watchdog cancellation must not overwrite a successful handler result")
}

// TestStreamIdleTimeout_WatchdogFireDoesNotMaskBusinessError covers the
// same hazard for a non-nil business error: it must propagate unchanged
// rather than being replaced by DeadlineExceeded.
func TestStreamIdleTimeout_WatchdogFireDoesNotMaskBusinessError(t *testing.T) {
	interc := interceptor.StreamIdleTimeout(20*time.Millisecond, nil)

	want := errors.New("application-specific failure")
	handler := func(_ any, ss grpc.ServerStream) error {
		<-ss.Context().Done()
		return want
	}

	info := &grpc.StreamServerInfo{FullMethod: "/test.Service/Stream"}
	ss := &fakeServerStream{ctx: context.Background()}

	err := interc(nil, ss, info, handler)
	require.ErrorIs(t, err, want,
		"watchdog cancellation must not overwrite a handler's business error")
}

// TestStreamIdleTimeout_PanicStopsWatchdogPromptly covers the resource
// leak where a handler panic unwinds past the watchdog teardown. With a
// plain (non-deferred) close(done) the panic skips it, so the watchdog
// goroutine and its ticker linger for the full idle duration. The
// watchdog must instead exit promptly when the deferred cancel fires.
func TestStreamIdleTimeout_PanicStopsWatchdogPromptly(t *testing.T) {
	// Long idle window so a lingering watchdog would obviously outlive
	// the short wait below.
	interc := interceptor.StreamIdleTimeout(10*time.Second, nil)

	handler := func(_ any, _ grpc.ServerStream) error {
		panic("boom")
	}

	info := &grpc.StreamServerInfo{FullMethod: "/test.Service/Stream"}

	before := runtime.NumGoroutine()
	const panics = 20
	for i := 0; i < panics; i++ {
		func() {
			defer func() {
				r := recover()
				require.NotNil(t, r, "panic must propagate to the outer recovery interceptor")
			}()
			_ = interc(nil, &fakeServerStream{ctx: context.Background()}, info, handler)
		}()
	}

	// Allow watchdogs that observe cancellation to exit, but far less
	// than the 10s idle window a lingering watchdog would wait out.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if runtime.NumGoroutine() <= before+2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	leaked := runtime.NumGoroutine() - before
	assert.LessOrEqual(t, leaked, 2,
		"panicked handlers must not leave watchdog goroutines lingering for the idle duration (leaked=%d)", leaked)
}

// TestStreamIdleTimeout_DoesNotUnblockHandlerParkedInRecvMsg pins the
// documented cooperative-cancellation limitation: a handler blocked inside
// the underlying stream's RecvMsg (a paused client that has gone silent) is
// NOT unblocked by the idle watchdog, because the watchdog cancels a context
// DERIVED from ss.Context() and grpc-go's transport-level RecvMsg blocks on
// the real parent stream context. Only handlers that select on
// Context().Done() are reaped (see TestStreamIdleTimeout_CancelsIdleStream).
//
// This exercises fakeServerStream.recvBlockCh / recvMsgCount, which model
// exactly that threat scenario, and converts the scaffolding into a live
// assertion of the contract.
func TestStreamIdleTimeout_DoesNotUnblockHandlerParkedInRecvMsg(t *testing.T) {
	interc := interceptor.StreamIdleTimeout(20*time.Millisecond, nil)

	block := make(chan struct{})
	ss := &fakeServerStream{ctx: context.Background(), recvBlockCh: block}
	watchdogFired := make(chan struct{})

	// Handler parks inside RecvMsg, ignoring its (derived, cancellable) ctx —
	// the canonical `for { stream.Recv() }` loop against a silent client.
	done := make(chan error, 1)
	go func() {
		done <- interc(nil, ss, &grpc.StreamServerInfo{FullMethod: "/test.Service/Stream"},
			func(_ any, stream grpc.ServerStream) error {
				go func() {
					<-stream.Context().Done()
					close(watchdogFired)
				}()
				return stream.RecvMsg(nil)
			},
		)
	}()

	// Observe the watchdog cancellation directly, then verify it did not
	// unblock the underlying RecvMsg call.
	select {
	case <-watchdogFired:
	case <-time.After(time.Second):
		t.Fatal("idle watchdog did not fire")
	}
	select {
	case err := <-done:
		t.Fatalf("handler parked in RecvMsg was unexpectedly unblocked by the idle watchdog (err=%v); the documented limitation no longer holds", err)
	default:
		// Expected: still blocked.
	}
	assert.Equal(t, int32(1), ss.recvMsgCount.Load(),
		"handler should be parked inside the single RecvMsg call")

	// Release so the goroutine and stream tear down cleanly.
	close(block)
	select {
	case err := <-done:
		require.NoError(t, err, "handler returns the RecvMsg result once the client speaks")
	case <-time.After(time.Second):
		t.Fatal("handler did not return after RecvMsg was unblocked")
	}
}

func TestNewStreamLimitMetrics_RegistersAllCollectors(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := interceptor.NewStreamLimitMetrics(interceptor.WithRegisterer(reg))
	require.NotNil(t, m)

	// Force at least one observation in each metric so Gather sees
	// the families.
	interc := interceptor.MaxConcurrentStreamsServer(1, m)
	release := make(chan struct{})
	entered := make(chan struct{})
	defer close(release)
	go func() {
		_ = interc(nil, &fakeServerStream{ctx: context.Background()},
			&grpc.StreamServerInfo{FullMethod: "/test/Stream"},
			func(_ any, _ grpc.ServerStream) error {
				close(entered)
				<-release
				return nil
			},
		)
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("stream did not enter handler")
	}

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
