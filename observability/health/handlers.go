package health

import (
	"encoding/json"
	"net/http"
)

// LivenessResponse is the JSON envelope returned by [Liveness].
type LivenessResponse struct {
	Status  string `json:"status"`
	Version string `json:"version,omitempty"`
}

// Liveness returns an [http.Handler] that always responds 200 OK with a
// minimal JSON body. This implements the Kubernetes liveness pattern: the
// handler exists to confirm the process is alive and the HTTP stack is
// serving — it does not check dependencies. If liveness probes fail,
// k8s restarts the pod, so this handler must not depend on databases,
// caches, or downstream services.
//
// Pass version (typically [ResolveVersion]) to include build information
// in the response for debugging mismatched-restart scenarios.
func Liveness(version string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(LivenessResponse{
			Status:  StatusHealthy,
			Version: version,
		})
	})
}

// Readiness returns an [http.Handler] that evaluates checker and responds:
//   - 200 OK when overall status is StatusHealthy or StatusDegraded
//     (degraded = non-critical dependency unhealthy, but the service can
//     still serve requests, so we stay in the load balancer rotation).
//   - 503 Service Unavailable when status is StatusUnhealthy
//     (a critical dependency failed; remove from rotation).
//
// This implements the Kubernetes readiness pattern: failed readiness
// probes pull the pod out of Service endpoints without restarting it.
//
// The body is a [Response] so operators get the full per-dependency
// breakdown.
func Readiness(checker *Checker) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := checker.Evaluate(r.Context())

		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")

		status := http.StatusOK
		if resp.Status == StatusUnhealthy {
			status = http.StatusServiceUnavailable
		}
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(resp)
	})
}
