package health

import (
	"encoding/json"
	"log/slog"
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
		body, err := json.Marshal(LivenessResponse{
			Status:  StatusHealthy,
			Version: version,
		})
		if err != nil {
			slog.Error("health: marshal liveness response", "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	})
}

// Readiness returns an [http.Handler] that evaluates checker and responds:
//   - 200 OK when overall status is StatusHealthy or StatusDegraded
//     (degraded = non-critical dependency unhealthy, but the service can
//     still serve requests, so we stay in the load balancer rotation).
//   - 503 Service Unavailable when status is StatusConnecting or
//     StatusUnhealthy. Connecting means "still establishing dependency
//     connections" — routing during warmup makes the first requests fail
//     with closed-pool / uninitialised-cache errors, so the LB must hold
//     traffic until warmup completes.
//
// This implements the Kubernetes readiness pattern: failed readiness
// probes pull the pod out of Service endpoints without restarting it.
//
// The body is a [Response] so operators get the full per-dependency
// breakdown. The body is marshalled before WriteHeader so encode errors
// surface as a 500 with no body, instead of a half-written 200/503.
func Readiness(checker *Checker) http.Handler {
	if checker == nil {
		panic("health: Readiness requires a non-nil *Checker")
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := checker.Evaluate(r.Context())

		body, err := json.Marshal(resp)
		if err != nil {
			slog.Error("health: marshal readiness response", "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")

		status := http.StatusOK
		switch resp.Status {
		case StatusUnhealthy, StatusConnecting:
			status = http.StatusServiceUnavailable
		}
		w.WriteHeader(status)
		_, _ = w.Write(body)
	})
}
