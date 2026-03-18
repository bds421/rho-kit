package maxbody

import "net/http"

// MaxBodySize returns middleware that limits request body size.
// Requests exceeding maxBytes will receive a 413 Request Entity Too Large
// response when the handler attempts to read the body.
// Panics if maxBytes is not positive — a zero or negative limit rejects all
// bodies, which is always a misconfiguration.
func MaxBodySize(maxBytes int64) func(http.Handler) http.Handler {
	if maxBytes <= 0 {
		panic("maxbody: maxBytes must be positive")
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Body != nil {
				r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
			}
			next.ServeHTTP(w, r)
		})
	}
}
