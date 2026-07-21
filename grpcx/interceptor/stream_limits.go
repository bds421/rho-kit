// Package interceptor — stream_limits.go: server-wide stream
// resource discipline.
//
// gRPC's built-in [grpc.MaxConcurrentStreams] caps streams per HTTP/2
// connection, not server-wide. A long-lived multiplexed client (or a
// fleet of them) can saturate a server with concurrent streams that
// each survive within the per-connection cap. Wave 166 adds a
// server-wide ceiling enforced by an interceptor, mirroring the
// httpx/websocket WithMaxConnections cap shipped in wave 157.
//
// StreamIdleTimeout closes streams that have neither sent nor
// received a message for the configured duration. gRPC's HTTP/2
// keepalive detects DEAD connections but does not police IDLE
// streams — a paused client can hold a server-side stream open
// indefinitely, accumulating goroutines under sustained load.

package interceptor

import (
	"context"
	"errors"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/bds421/rho-kit/observability/v2/promutil"
)

// StreamLimitMetrics holds Prometheus collectors emitted by
// [MaxConcurrentStreamsServer] and [StreamIdleTimeout]. Construct
// via [NewStreamLimitMetrics] (or omit metrics entirely for a
// silent no-op).
type StreamLimitMetrics struct {
	active    prometheus.Gauge
	rejected  *prometheus.CounterVec
	idleClose prometheus.Counter
}

// NewStreamLimitMetrics creates and registers the stream-limit
// metric set on the supplied registerer (or
// [prometheus.DefaultRegisterer]). Repeated calls reuse already-
// registered collectors.
func NewStreamLimitMetrics(opts ...MetricsOption) *StreamLimitMetrics {
	cfg := metricsConfig{registerer: prometheus.DefaultRegisterer}
	for _, opt := range opts {
		if opt == nil {
			panic("grpcx/interceptor: NewStreamLimitMetrics option must not be nil")
		}
		opt(&cfg)
	}
	reg := cfg.registerer
	// Namespace="grpc", Subsystem="server": preserves wire-form
	// names while aligning with the kit's Namespace+Subsystem+Name
	// convention.
	m := &StreamLimitMetrics{
		active: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "grpc",
			Subsystem: "server",
			Name:      "active_streams",
			Help:      "Number of currently-open streaming RPCs handled by the server.",
		}),
		rejected: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "grpc",
			Subsystem: "server",
			Name:      "streams_rejected_total",
			Help:      "Total streaming RPCs rejected before handler entry by reason (bounded reason enum).",
		}, []string{"reason"}),
		idleClose: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "grpc",
			Subsystem: "server",
			Name:      "streams_idle_closed_total",
			Help:      "Total streaming RPCs cancelled by the kit-level idle-timeout watchdog.",
		}),
	}
	m.active = promutil.MustRegisterOrGet(reg, m.active)
	m.rejected = promutil.MustRegisterOrGet(reg, m.rejected)
	m.idleClose = promutil.MustRegisterOrGet(reg, m.idleClose)
	return m
}

const (
	streamRejectReasonMaxConcurrent = "max_concurrent"
)

// MaxConcurrentStreamsServer returns a [grpc.StreamServerInterceptor]
// that caps the number of concurrent streaming RPCs handled by this
// server across ALL client connections. Beyond the cap, new streams
// are rejected with `codes.ResourceExhausted` before the registered
// handler runs.
//
// gRPC's built-in [grpc.MaxConcurrentStreams] is a per-HTTP/2-
// connection cap; this interceptor adds a server-wide cap so a
// fleet of well-behaved clients cannot collectively saturate the
// server.
//
// Panics on max <= 0 so misconfiguration surfaces at startup. Pass
// nil metrics to omit observability.
func MaxConcurrentStreamsServer(max int, metrics *StreamLimitMetrics) grpc.StreamServerInterceptor {
	if max <= 0 {
		panic("grpcx/interceptor: MaxConcurrentStreamsServer requires a positive max")
	}
	limiter := newStreamLimiter(int64(max))
	return func(srv any, ss grpc.ServerStream, _ *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		if !limiter.tryAcquire() {
			if metrics != nil {
				metrics.rejected.WithLabelValues(streamRejectReasonMaxConcurrent).Inc()
			}
			return status.Error(codes.ResourceExhausted, "grpc: server at concurrent-stream capacity")
		}
		if metrics != nil {
			metrics.active.Inc()
		}
		defer func() {
			limiter.release()
			if metrics != nil {
				metrics.active.Dec()
			}
		}()
		return handler(srv, ss)
	}
}

// StreamIdleTimeout returns a [grpc.StreamServerInterceptor] that
// cancels the stream's context if no message has been sent or
// received within the configured duration. The kit's mirror of the
// [httpx/websocket] WithPingInterval reaper — gRPC's HTTP/2
// keepalive detects DEAD peers but not IDLE streams that simply
// stop talking.
//
// Implementation: wraps the [grpc.ServerStream] so SendMsg / RecvMsg
// reset a per-stream "last activity" timestamp. A watchdog goroutine
// ticks at the configured interval and cancels the stream context
// when (now - last activity) exceeds the timeout. The watchdog
// exits as soon as the handler returns to keep the per-stream
// goroutine footprint at 1.
//
// Cooperative-cancellation contract (IMPORTANT): the watchdog cancels
// a context DERIVED from ss.Context(); it does not abort the
// underlying HTTP/2 stream. grpc-go's transport-level RecvMsg blocks
// on the real (parent) stream context, so cancelling the derived
// child does NOT unblock a handler parked in stream.Recv(). The
// canonical `for { stream.Recv() }` loop against a paused client will
// therefore stay blocked until the client speaks or the connection
// dies. Only handlers that explicitly select on Context().Done()
// (or otherwise observe the wrapped context) are reaped by this
// watchdog. For hard bounds against unresponsive peers, pair this
// interceptor with server-side [keepalive.ServerParameters] (notably
// MaxConnectionAge / MaxConnectionAgeGrace) and
// [keepalive.EnforcementPolicy].
//
// Panics on d <= 0.
func StreamIdleTimeout(d time.Duration, metrics *StreamLimitMetrics) grpc.StreamServerInterceptor {
	if d <= 0 {
		panic("grpcx/interceptor: StreamIdleTimeout requires a positive duration")
	}
	return func(srv any, ss grpc.ServerStream, _ *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		ctx, cancel := context.WithCancel(ss.Context())
		defer cancel()

		wrapped := &idleStream{
			ServerStream: ss,
			ctx:          ctx,
			lastActive:   atomic.Int64{},
		}
		wrapped.lastActive.Store(monoNow())

		// Watchdog: poll at 1/4 the idle timeout so the worst-case
		// detection latency is +25%. Exits as soon as the handler
		// returns (done) or the context is cancelled (ctx.Done) — the
		// latter also covers a handler panic unwinding past the close
		// below, where only the deferred cancel runs.
		done := make(chan struct{})
		// Deferred so a handler panic still tears the watchdog down
		// promptly instead of leaving the goroutine and ticker lingering
		// until the idle window elapses.
		defer close(done)
		go func() {
			tick := d / 4
			if tick < 10*time.Millisecond {
				tick = 10 * time.Millisecond
			}
			t := time.NewTicker(tick)
			defer t.Stop()
			for {
				select {
				case <-done:
					return
				case <-ctx.Done():
					return
				case <-t.C:
					// Re-check done first so a handler that just returned does
					// not spuriously increment streams_idle_closed_total.
					select {
					case <-done:
						return
					default:
					}
					last := wrapped.lastActive.Load()
					// lastActive stores mono-ish nanos from a process-start
					// origin (see idleStream.touch) so NTP wall steps cannot
					// mass-cancel streams.
					if monoNow()-last >= d.Nanoseconds() {
						if metrics != nil {
							metrics.idleClose.Inc()
						}
						cancel()
						return
					}
				}
			}
		}()

		err := handler(srv, wrapped)
		// If our watchdog cancelled the ctx (vs the caller cancelling
		// upstream), surface a gRPC-friendly DeadlineExceeded — but only
		// when the handler actually failed because of that cancellation.
		// A handler that ignored ctx and still returned cleanly (e.g. a
		// client-streaming RPC already replied via SendAndClose) or with
		// a business error must keep its own result; overwriting it would
		// report a delivered response as failed and invite duplicate
		// client retries.
		if err != nil && idleTimeoutRemap(err) &&
			ctx.Err() != nil && errors.Is(ctx.Err(), context.Canceled) {
			// Distinguish "we cancelled" from "caller cancelled". The
			// wrapper's ctx is derived from ss.Context(); if the
			// underlying stream is also cancelled, the caller did
			// it, not us.
			if ss.Context().Err() == nil {
				return status.Error(codes.DeadlineExceeded, "grpc: stream idle timeout")
			}
		}
		return err
	}
}

// idleStream wraps a [grpc.ServerStream] so SendMsg / RecvMsg reset
// the per-stream last-activity timestamp the watchdog polls. All
// other methods delegate unchanged.
type idleStream struct {
	grpc.ServerStream
	ctx        context.Context
	lastActive atomic.Int64
}

func (s *idleStream) Context() context.Context { return s.ctx }

func (s *idleStream) SendMsg(m any) error {
	s.lastActive.Store(monoNow())
	return s.ServerStream.SendMsg(m)
}

func (s *idleStream) RecvMsg(m any) error {
	err := s.ServerStream.RecvMsg(m)
	s.lastActive.Store(monoNow())
	return err
}

// streamLimiter is the same CAS-loop counter as
// httpx/websocket/limiter.go. Duplicating here rather than sharing
// keeps the grpcx module free of an internal-package dependency.
type streamLimiter struct {
	max     int64
	current atomic.Int64
}

func newStreamLimiter(max int64) *streamLimiter {
	return &streamLimiter{max: max}
}

func (l *streamLimiter) tryAcquire() bool {
	for {
		cur := l.current.Load()
		if cur >= l.max {
			return false
		}
		if l.current.CompareAndSwap(cur, cur+1) {
			return true
		}
	}
}

func (l *streamLimiter) release() { l.current.Add(-1) }

// processStart anchors monotonic-ish idle timing so wall-clock NTP steps
// cannot mass-cancel active streams. time.Since(processStart) uses the
// monotonic reading of processStart when available.
var processStart = time.Now()

func monoNow() int64 { return time.Since(processStart).Nanoseconds() }

// idleTimeoutRemap reports whether err should be remapped to DeadlineExceeded
// when the idle watchdog cancelled the stream. Matches raw context.Canceled
// and status-wrapped codes.Canceled (idiomatic FromContextError conversions).
func idleTimeoutRemap(err error) bool {
	if errors.Is(err, context.Canceled) {
		return true
	}
	if st, ok := status.FromError(err); ok && st.Code() == codes.Canceled {
		return true
	}
	return false
}
