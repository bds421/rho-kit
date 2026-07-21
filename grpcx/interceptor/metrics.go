package interceptor

import (
	"context"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/bds421/rho-kit/observability/v2/promutil"
)

// GRPCMetrics holds Prometheus collectors for gRPC server monitoring.
// Thread-safe after construction.
type GRPCMetrics struct {
	handledTotal    *prometheus.CounterVec
	handlingSeconds *prometheus.HistogramVec
}

// MetricsOption configures [NewMetrics].
type MetricsOption func(*metricsConfig)

type metricsConfig struct {
	registerer prometheus.Registerer
}

// WithRegisterer pins the Prometheus registerer used for gRPC server
// metrics. Unset defaults to [prometheus.DefaultRegisterer]; passing
// nil panics.
func WithRegisterer(reg prometheus.Registerer) MetricsOption {
	if reg == nil {
		panic("grpcx/interceptor: WithRegisterer requires a non-nil registerer (omit the option for DefaultRegisterer)")
	}
	return func(c *metricsConfig) { c.registerer = reg }
}

// NewMetrics creates and registers gRPC server metrics. Pass
// [WithRegisterer] to use a non-default registry.
//
// Registered metrics:
//   - grpc_server_handled_total: counter with labels {grpc_method, grpc_code}
//   - grpc_server_handling_seconds: histogram with labels {grpc_method}
//
// Replaces the v1 NewGRPCMetrics(reg) spelling so the constructor
// signature matches the kit-wide options-based shape.
func NewMetrics(opts ...MetricsOption) *GRPCMetrics {
	cfg := metricsConfig{registerer: prometheus.DefaultRegisterer}
	for _, opt := range opts {
		if opt == nil {
			panic("grpcx/interceptor: NewMetrics option must not be nil")
		}
		opt(&cfg)
	}
	reg := cfg.registerer

	// Namespace="grpc", Subsystem="server" preserves the wire-form
	// names (grpc_server_handled_total / grpc_server_handling_seconds)
	// while aligning the Go struct shape with the kit's
	// Namespace+Subsystem+Name convention.
	m := &GRPCMetrics{
		handledTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: "grpc",
				Subsystem: "server",
				Name:      "handled_total",
				Help:      "Total number of gRPC calls handled by the server.",
			},
			[]string{"grpc_method", "grpc_code"},
		),
		handlingSeconds: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace: "grpc",
				Subsystem: "server",
				Name:      "handling_seconds",
				Help:      "Histogram of gRPC call handling duration in seconds.",
				Buckets:   prometheus.DefBuckets,
			},
			[]string{"grpc_method"},
		),
	}

	m.handledTotal = promutil.MustRegisterOrGet(reg, m.handledTotal)
	m.handlingSeconds = promutil.MustRegisterOrGet(reg, m.handlingSeconds)

	return m
}

// UnaryInterceptor returns a unary server interceptor that records metrics.
func (m *GRPCMetrics) UnaryInterceptor() grpc.UnaryServerInterceptor {
	return func(
		ctx context.Context,
		req any,
		info *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (resp any, err error) {
		start := time.Now()
		defer func() {
			if rec := recover(); rec != nil {
				m.record(info.FullMethod, status.Error(codes.Internal, "panic"), time.Since(start))
				panic(rec)
			}
			m.record(info.FullMethod, err, time.Since(start))
		}()
		resp, err = handler(ctx, req)
		return resp, err
	}
}

// StreamInterceptor returns a stream server interceptor that records metrics.
func (m *GRPCMetrics) StreamInterceptor() grpc.StreamServerInterceptor {
	return func(
		srv any,
		ss grpc.ServerStream,
		info *grpc.StreamServerInfo,
		handler grpc.StreamHandler,
	) (err error) {
		start := time.Now()
		defer func() {
			if rec := recover(); rec != nil {
				m.record(info.FullMethod, status.Error(codes.Internal, "panic"), time.Since(start))
				panic(rec)
			}
			m.record(info.FullMethod, err, time.Since(start))
		}()
		err = handler(srv, ss)
		return err
	}
}

// record updates metrics for a completed RPC.
func (m *GRPCMetrics) record(method string, err error, duration time.Duration) {
	code := statusCode(err)
	method = grpcMethodLabel(method)
	m.handledTotal.WithLabelValues(method, code).Inc()
	m.handlingSeconds.WithLabelValues(method).Observe(duration.Seconds())
}

func grpcMethodLabel(method string) string {
	if err := promutil.ValidateStaticLabelValue("grpc method", method); err != nil {
		return "invalid"
	}
	return method
}

// statusCode extracts the gRPC status code string from an error.
func statusCode(err error) string {
	if err == nil {
		return "OK"
	}
	st, ok := status.FromError(err)
	if ok {
		return st.Code().String()
	}
	return "Unknown"
}
