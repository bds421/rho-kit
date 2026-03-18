package httpx

import "net/http"

// ParseBoolParam extracts an optional boolean query parameter.
// Returns nil if the parameter is absent or not a valid boolean.
func ParseBoolParam(r *http.Request, key string) *bool {
	v := r.URL.Query().Get(key)
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
