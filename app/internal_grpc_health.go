package app

import (
	"net/http"
	"strings"
	"unicode/utf8"

	"golang.org/x/net/http/httpguts"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
	"google.golang.org/grpc"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"

	"github.com/bds421/rho-kit/grpcx/v2"
	"github.com/bds421/rho-kit/observability/v2/health"
)

func withInternalGRPCHealth(base http.Handler, checker *health.Checker) http.Handler {
	if base == nil {
		panic("app: internal handler must not be nil")
	}
	if checker == nil {
		return base
	}

	grpcHealth := grpc.NewServer()
	healthpb.RegisterHealthServer(grpcHealth, grpcx.NewHealthServer(checker))

	return h2c.NewHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if internalGRPCHealthRequest(r) {
			grpcHealth.ServeHTTP(w, r)
			return
		}
		base.ServeHTTP(w, r)
	}), &http2.Server{})
}

func internalGRPCHealthRequest(r *http.Request) bool {
	if r == nil || r.ProtoMajor != 2 {
		return false
	}
	values := r.Header.Values("Content-Type")
	if len(values) != 1 {
		return false
	}
	contentType := values[0]
	if contentType == "" || !utf8.ValidString(contentType) || !httpguts.ValidHeaderFieldValue(contentType) {
		return false
	}
	contentType = strings.ToLower(strings.TrimSpace(contentType))
	if contentType == "application/grpc" {
		return true
	}
	if !strings.HasPrefix(contentType, "application/grpc") {
		return false
	}
	switch contentType[len("application/grpc")] {
	case '+', ';':
		return true
	default:
		return false
	}
}
