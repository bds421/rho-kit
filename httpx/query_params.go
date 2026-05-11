package httpx

import "net/http"

// ParseBoolParam extracts an optional boolean query parameter.
// Returns nil if the parameter is absent, ambiguous, or not a valid boolean.
func ParseBoolParam(r *http.Request, key string) *bool {
	if r == nil || r.URL == nil || key == "" {
		return nil
	}
	values := r.URL.Query()[key]
	if len(values) != 1 {
		return nil
	}
	v := values[0]
	switch v {
	case "true", "1":
		b := true
		return &b
	case "false", "0":
		b := false
		return &b
	default:
		return nil
	}
}
