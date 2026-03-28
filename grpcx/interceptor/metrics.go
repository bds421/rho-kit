package interceptor

import (
	"context"
	"errors"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/status"
)

// GRPCMetrics holds Prometheus collectors for gRPC server monitoring.
// Thread-safe after construction.
type GRPCMetrics struct {
	handledTotal    *prometheus.CounterVec
	handlingSeconds *prometheus.HistogramVec
}

// NewGRPCMetrics creates and registers gRPC server metrics with the given
// registerer. If reg is nil, prometheus.DefaultRegisterer is used.
//
// Registered metrics:
//   - grpc_server_handled_total: counter with labels {grpc_method, grpc_code}
//   - grpc_server_handling_seconds: histogram with labels {grpc_method}
func NewGRPCMetrics(reg prometheus.Registerer) *GRPCMetrics {
	if reg == nil {
		reg = prometheus.DefaultRegisterer
	}

	m := &GRPCMetrics{
		handledTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "grpc_server_handled_total",
				Help: "Total number of gRPC calls handled by the server.",
			},
			[]string{"grpc_method", "grpc_code"},
		),
		handlingSeconds: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "grpc_server_handling_seconds",
				Help:    "Histogram of gRPC call handling duration in seconds.",
				Buckets: prometheus.DefBuckets,
			},
			[]string{"grpc_method"},
		),
	}

	m.handledTotal = tryRegister(reg, m.handledTotal).(*prometheus.CounterVec)
	m.handlingSeconds = tryRegister(reg, m.handlingSeconds).(*prometheus.HistogramVec)

	return m
}

// UnaryInterceptor returns a unary server interceptor that records metrics.
func (m *GRPCMetrics) UnaryInterceptor() grpc.UnaryServerInterceptor {
	return func(
		ctx context.Context,
		req any,
		info *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (any, error) {
		start := time.Now()
		resp, err := handler(ctx, req)
		m.record(info.FullMethod, err, time.Since(start))
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
	) error {
		start := time.Now()
		err := handler(srv, ss)
		m.record(info.FullMethod, err, time.Since(start))
		return err
	}
}

// record updates metrics for a completed RPC.
func (m *GRPCMetrics) record(method string, err error, duration time.Duration) {
	code := statusCode(err)
	m.handledTotal.WithLabelValues(method, code).Inc()
	m.handlingSeconds.WithLabelValues(method).Observe(duration.Seconds())
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

// tryRegister attempts to register a Prometheus collector. If it is already
// registered, the existing collector is returned. This prevents panics when
// the same metrics are created multiple times with the same registerer.
func tryRegister(reg prometheus.Registerer, c prometheus.Collector) prometheus.Collector {
	if err := reg.Register(c); err != nil {
		var are prometheus.AlreadyRegisteredError
		if errors.As(err, &are) {
			return are.ExistingCollector
		}
		panic(err)
	}
	return c
}
