// Package slohttp provides an HTTP handler for exposing SLO evaluation results
// as JSON. It adapts the transport-agnostic observability/slo package for HTTP.
package slohttp

import (
	"encoding/json"
	"math"
	"net/http"

	"github.com/bds421/rho-kit/observability/slo"
)

// StatusResponse is the JSON envelope for the /slo endpoint.
type StatusResponse struct {
	Statuses []StatusJSON `json:"statuses"`
	Overall  string       `json:"overall"` // "ok" or "breached"
}

// StatusJSON is the JSON-serialisable form of slo.SLOStatus with Window as a
// human-readable string. Current is a pointer so that NaN values (from missing
// metrics) serialise as JSON null instead of producing invalid JSON.
type StatusJSON struct {
	Name      string      `json:"name"`
	Type      slo.SLOType `json:"type"`
	Threshold float64     `json:"threshold"`
	Current   *float64    `json:"current"`
	Breached  bool        `json:"breached"`
	BurnRate  float64     `json:"burn_rate"`
	Window    string      `json:"window"`
}

func toJSON(s slo.SLOStatus) StatusJSON {
	var current *float64
	if !math.IsNaN(s.Current) {
		v := s.Current
		current = &v
	}
	return StatusJSON{
		Name:      s.Name,
		Type:      s.Type,
		Threshold: s.Threshold,
		Current:   current,
		Breached:  s.Breached,
		BurnRate:  s.BurnRate,
		Window:    s.Window.String(),
	}
}

// Handler returns an http.Handler that evaluates all SLOs and writes JSON.
// Only GET and HEAD are allowed; other methods receive 405.
// HTTP status is always 200 — SLO breach is informational, not an error.
//
// Panics if checker is nil (configuration error, caught at startup).
func Handler(checker *slo.Checker) http.Handler {
	if checker == nil {
		panic("slohttp: checker must not be nil")
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		statuses := checker.Evaluate()
		resp := buildResponse(statuses)

		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(resp)
	})
}

func buildResponse(statuses []slo.SLOStatus) StatusResponse {
	jsonStatuses := make([]StatusJSON, 0, len(statuses))
	overall := "ok"

	for _, s := range statuses {
		jsonStatuses = append(jsonStatuses, toJSON(s))
		if s.Breached {
			overall = "breached"
		}
	}

	return StatusResponse{
		Statuses: jsonStatuses,
		Overall:  overall,
	}
}
