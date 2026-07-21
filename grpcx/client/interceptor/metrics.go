package interceptor

import (
	"context"
	"errors"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"google.golang.org/grpc"

	"github.com/bds421/rho-kit/observability/v2/promutil"
)

// ClientMetrics holds Prometheus collectors for client-side gRPC
// monitoring. Mirrors the server-side surface but registers under the
// "grpc_client_*" name family so dashboards can distinguish them.
type ClientMetrics struct {
	handledTotal    *prometheus.CounterVec
	handlingSeconds *prometheus.HistogramVec
}

// MetricsOption configures [NewMetrics].
type MetricsOption func(*metricsConfig)

type metricsConfig struct {
	registerer prometheus.Registerer
}

// WithRegisterer pins the Prometheus registerer used for gRPC client
// metrics. Unset defaults to [prometheus.DefaultRegisterer]; passing
// nil panics.
func WithRegisterer(reg prometheus.Registerer) MetricsOption {
	if reg == nil {
		panic("grpcx/client/interceptor: WithRegisterer requires a non-nil registerer")
	}
	return func(c *metricsConfig) { c.registerer = reg }
}

// NewMetrics creates and registers gRPC client metrics. Registered:
//
//   - grpc_client_handled_total{grpc_method, grpc_code}
//   - grpc_client_handling_seconds{grpc_method}
func NewMetrics(opts ...MetricsOption) *ClientMetrics {
	cfg := metricsConfig{registerer: prometheus.DefaultRegisterer}
	for _, opt := range opts {
		if opt == nil {
			panic("grpcx/client/interceptor: NewMetrics option must not be nil")
		}
		opt(&cfg)
	}
	reg := cfg.registerer

	m := &ClientMetrics{
		handledTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: "grpc",
				Subsystem: "client",
				Name:      "handled_total",
				Help:      "Total number of gRPC calls issued by the client.",
			},
			[]string{"grpc_method", "grpc_code"},
		),
		handlingSeconds: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace: "grpc",
				Subsystem: "client",
				Name:      "handling_seconds",
				Help:      "Histogram of gRPC client call duration in seconds.",
				Buckets:   prometheus.DefBuckets,
			},
			[]string{"grpc_method"},
		),
	}
	m.handledTotal = tryRegister(reg, m.handledTotal).(*prometheus.CounterVec)
	m.handlingSeconds = tryRegister(reg, m.handlingSeconds).(*prometheus.HistogramVec)
	return m
}

// UnaryInterceptor returns a unary client interceptor that records
// metrics for every call.
func (m *ClientMetrics) UnaryInterceptor() grpc.UnaryClientInterceptor {
	return func(
		ctx context.Context,
		method string,
		req, reply any,
		cc *grpc.ClientConn,
		invoker grpc.UnaryInvoker,
		opts ...grpc.CallOption,
	) error {
		start := time.Now()
		err := invoker(ctx, method, req, reply, cc, opts...)
		m.record(method, err, time.Since(start))
		return err
	}
}

// StreamInterceptor returns a stream client interceptor. Records on
// stream construction (success/error); per-message metrics belong on
// the caller side via standard counters.
func (m *ClientMetrics) StreamInterceptor() grpc.StreamClientInterceptor {
	return func(
		ctx context.Context,
		desc *grpc.StreamDesc,
		cc *grpc.ClientConn,
		method string,
		streamer grpc.Streamer,
		opts ...grpc.CallOption,
	) (grpc.ClientStream, error) {
		start := time.Now()
		cs, err := streamer(ctx, desc, cc, method, opts...)
		m.record(method, err, time.Since(start))
		return cs, err
	}
}

func (m *ClientMetrics) record(method string, err error, duration time.Duration) {
	method = methodLabel(method)
	m.handledTotal.WithLabelValues(method, codeName(err)).Inc()
	m.handlingSeconds.WithLabelValues(method).Observe(duration.Seconds())
}

func methodLabel(method string) string {
	if err := promutil.ValidateStaticLabelValue("grpc method", method); err != nil {
		return "invalid"
	}
	return method
}

func tryRegister(reg prometheus.Registerer, c prometheus.Collector) prometheus.Collector {
	if err := reg.Register(c); err != nil {
		var are prometheus.AlreadyRegisteredError
		if errors.As(err, &are) {
			return are.ExistingCollector
		}
		panic("grpcx/client/interceptor: metric registration failed: " + err.Error())
	}
	return c
}
