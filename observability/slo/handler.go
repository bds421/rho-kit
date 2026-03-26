package slo

import (
	"encoding/json"
	"net/http"
)

// StatusResponse is the JSON envelope for the /slo endpoint.
type StatusResponse struct {
	Statuses []SLOStatusJSON `json:"statuses"`
	Overall  string          `json:"overall"` // "ok" or "breached"
}

// SLOStatusJSON is the JSON-serialisable form of SLOStatus with Window as a
// human-readable string instead of nanoseconds.
type SLOStatusJSON struct {
	Name      string  `json:"name"`
	Type      SLOType `json:"type"`
	Threshold float64 `json:"threshold"`
	Current   float64 `json:"current"`
	Breached  bool    `json:"breached"`
	BurnRate  float64 `json:"burn_rate"`
	Window    string  `json:"window"`
}

// toJSON converts an SLOStatus to its JSON-friendly representation.
func toJSON(s SLOStatus) SLOStatusJSON {
	return SLOStatusJSON{
		Name:      s.Name,
		Type:      s.Type,
		Threshold: s.Threshold,
		Current:   s.Current,
		Breached:  s.Breached,
		BurnRate:  s.BurnRate,
		Window:    s.Window.String(),
	}
}

// Handler returns an http.Handler that evaluates all SLOs and writes the result
// as JSON. The response includes each SLO's status and an overall "ok"/"breached"
// indicator. Only GET and HEAD methods are allowed; other methods receive 405.
//
// Returns HTTP 200 when all SLOs are within budget, HTTP 200 with overall="breached"
// when any SLO is violated. The HTTP status is always 200 because SLO breach is
// informational -- use [Checker.HealthCheck] for readiness integration.
//
// Panics if checker is nil. This is a configuration error that should be caught
// at startup.
func Handler(checker *Checker) http.Handler {
	if checker == nil {
		panic("slo: checker must not be nil")
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
		_ = json.NewEncoder(w).Encode(resp)
	})
}

// buildResponse constructs a StatusResponse from evaluated SLO statuses.
func buildResponse(statuses []SLOStatus) StatusResponse {
	jsonStatuses := make([]SLOStatusJSON, 0, len(statuses))
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
