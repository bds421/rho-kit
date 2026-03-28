package grpcx

import (
	"google.golang.org/grpc"

	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
)

// WithTracingStatsHandler returns a ServerOption that adds the otelgrpc stats
// handler for OpenTelemetry tracing. This is the preferred approach over
// interceptors for gRPC tracing instrumentation.
//
// Options are forwarded to otelgrpc.NewServerHandler. Common options include
// otelgrpc.WithTracerProvider and otelgrpc.WithMeterProvider.
func WithTracingStatsHandler(opts ...otelgrpc.Option) ServerOption {
	handler := otelgrpc.NewServerHandler(opts...)
	return WithGRPCServerOptions(grpc.StatsHandler(handler))
}
