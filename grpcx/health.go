package grpcx

import (
	"context"

	"github.com/bds421/rho-kit/observability/health"
	"google.golang.org/grpc/codes"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
)

// HealthServer bridges the kit's health.Checker to the gRPC Health Checking
// Protocol (grpc.health.v1). This enables Kubernetes gRPC liveness/readiness
// probes and load balancer health checks over the gRPC transport.
//
// The Check method evaluates the health checker and maps the result:
//   - StatusHealthy / StatusDegraded / StatusConnecting → SERVING
//   - StatusUnhealthy → NOT_SERVING
//
// The Watch method is not implemented; it returns Unimplemented. Most
// Kubernetes probes and load balancers use Check, not Watch.
type HealthServer struct {
	healthpb.UnimplementedHealthServer
	checker *health.Checker
}

// NewHealthServer creates a HealthServer that delegates to the given checker.
// Panics if checker is nil to fail fast on misconfiguration.
func NewHealthServer(checker *health.Checker) *HealthServer {
	if checker == nil {
		panic("grpcx: NewHealthServer requires a non-nil health.Checker")
	}
	return &HealthServer{checker: checker}
}

// Check evaluates the health checker and returns the gRPC health status.
// An empty or unrecognized service name checks overall health (same as
// the HTTP /ready endpoint). Named service checks are not supported and
// return NOT_FOUND.
func (h *HealthServer) Check(ctx context.Context, req *healthpb.HealthCheckRequest) (*healthpb.HealthCheckResponse, error) {
	if req.GetService() != "" {
		return nil, status.Error(codes.NotFound, "unknown service")
	}

	resp := h.checker.Evaluate(ctx)
	servingStatus := healthpb.HealthCheckResponse_SERVING
	if resp.Status == health.StatusUnhealthy {
		servingStatus = healthpb.HealthCheckResponse_NOT_SERVING
	}

	return &healthpb.HealthCheckResponse{
		Status: servingStatus,
	}, nil
}

// Watch is not implemented. Most Kubernetes probes use Check (unary), not
// Watch (streaming). Returns Unimplemented to signal clients clearly.
func (h *HealthServer) Watch(_ *healthpb.HealthCheckRequest, _ healthpb.Health_WatchServer) error {
	return status.Error(codes.Unimplemented, "watch is not implemented")
}
