// asvs: V13.4.1
package maxbody

import "net/http"

// MaxBodySize returns middleware that limits request body size via
// [http.MaxBytesReader]. Reads beyond maxBytes fail with
// *[http.MaxBytesError]; the middleware does not write a status itself,
// so the response code depends on the handler. The kit's decode helpers
// (e.g. httpx.DecodeJSON) translate that error into HTTP 413 Request
// Entity Too Large.
// Panics if maxBytes is not positive — a zero or negative limit rejects all
// bodies, which is always a misconfiguration.
func MaxBodySize(maxBytes int64) func(http.Handler) http.Handler {
	if maxBytes <= 0 {
		panic("maxbody: MaxBodySize maxBytes must be positive")
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
