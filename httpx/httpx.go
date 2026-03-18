package httpx

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

// NewHTTPClient returns an *http.Client with the given timeout and optional TLS
// configuration. When tlsConfig is non-nil the client trusts the internal CA,
// ensuring all inter-service HTTPS calls work under mTLS.
// Use this instead of &http.Client{} to avoid accidentally creating a client
// that cannot verify the internal PKI certificates.
//
// The transport is cloned from http.DefaultTransport to inherit production
// defaults (idle connection management, TLS handshake timeout, proxy support).
func NewHTTPClient(timeout time.Duration, tlsConfig *tls.Config) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if tlsConfig != nil {
		transport.TLSClientConfig = tlsConfig
	}
	return &http.Client{
		Timeout:   timeout,
		Transport: transport,
	}
}

// NewTracingHTTPClient returns an *http.Client instrumented with OpenTelemetry
// spans for outbound requests. It uses the same TLS setup as NewHTTPClient.
func NewTracingHTTPClient(timeout time.Duration, tlsConfig *tls.Config, opts ...otelhttp.Option) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if tlsConfig != nil {
		transport.TLSClientConfig = tlsConfig
	}
	return &http.Client{
		Timeout:   timeout,
		Transport: otelhttp.NewTransport(transport, opts...),
	}
}

// ServerOption customises the *http.Server returned by NewServer.
type ServerOption func(*http.Server)

// WithWriteTimeout overrides the default write timeout.
// Use 0 for WebSocket servers where pumps manage their own deadlines.
func WithWriteTimeout(d time.Duration) ServerOption {
	return func(s *http.Server) { s.WriteTimeout = d }
}

// WithTLSConfig sets the server TLS configuration for mTLS.
// When set, lifecycle.HTTPServer uses ListenAndServeTLS instead of ListenAndServe.
func WithTLSConfig(cfg *tls.Config) ServerOption {
	return func(s *http.Server) { s.TLSConfig = cfg }
}

// NewServer returns an *http.Server with safe production defaults.
// Options may override individual fields.
func NewServer(addr string, handler http.Handler, opts ...ServerOption) *http.Server {
	srv := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadTimeout:       15 * time.Second,
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      35 * time.Second, // Must exceed the configured request timeout so middleware can write 503
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20, // 1 MB
	}
	for _, opt := range opts {
		opt(srv)
	}
	return srv
}

// MaxBodySize is the maximum allowed JSON request body (1 MB).
const MaxBodySize = 1 << 20

// APIError is the standard error response envelope.
type APIError struct {
	Error string `json:"error"`
	Code  string `json:"code"`
}

// WriteJSON writes a JSON response with the given status code.
// If JSON encoding fails, it returns a 500 with a safe error body.
//
// Error responses (4xx/5xx) automatically include Cache-Control: no-store to
// prevent CDNs or browsers from caching error states. Success responses do not
// set Cache-Control, allowing callers to set appropriate caching headers.
//
// The response is fully buffered before writing to allow correct error handling:
// if marshalling fails, a 500 can still be sent since no bytes have been written.
// For large paginated responses consider a streaming variant.
//
// Write failures are logged at Warn level. Use [WriteJSONCtx] when a
// request context is available for request-scoped logging.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	writeJSONInternal(w, status, v, slog.Default())
}

// WriteJSONCtx is like [WriteJSON] but uses the request-scoped logger from ctx.
// Prefer this over WriteJSON inside HTTP handlers for proper log correlation.
func WriteJSONCtx(ctx context.Context, w http.ResponseWriter, status int, v any) {
	writeJSONInternal(w, status, v, Logger(ctx, slog.Default()))
}

func writeJSONInternal(w http.ResponseWriter, status int, v any, logger *slog.Logger) {
	w.Header().Set("Content-Type", "application/json")
	if status >= 400 {
		w.Header().Set("Cache-Control", "no-store")
	}
	buf, err := json.Marshal(v)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"internal error","code":"INTERNAL"}` + "\n"))
		return
	}
	w.WriteHeader(status)
	if _, err = w.Write(buf); err != nil {
		logger.Warn("httpx: response write failed", "error", err)
		return
	}
	if _, err = w.Write([]byte("\n")); err != nil {
		logger.Warn("httpx: response write failed", "error", err)
	}
}

// WriteError writes a JSON error response with a machine-readable code
// derived from the HTTP status.
func WriteError(w http.ResponseWriter, status int, msg string) {
	WriteJSON(w, status, APIError{Error: msg, Code: httpStatusToCode(status)})
}

// ParseID extracts a uint "id" path parameter from the request.
// Returns (0, false) for ID 0 since auto-increment databases never use it.
func ParseID(r *http.Request) (uint, bool) {
	idStr := r.PathValue("id")
	id, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil || id == 0 {
		return 0, false
	}
	return uint(id), true
}

// DecodeJSON reads and decodes a JSON request body with a size limit.
// Returns false and writes an error response if decoding fails.
//
// Unknown fields in the JSON body are rejected (DisallowUnknownFields).
// This is intentional: strict parsing catches client-side typos early and
// prevents silent data loss. If forward-compatible parsing is needed (e.g.,
// for versioned APIs where clients may send newer fields), use a custom
// decoder without DisallowUnknownFields.
//
// Note: this replaces r.Body with an http.MaxBytesReader wrapper. Any
// subsequent reads of r.Body will go through the size-limited reader.
func DecodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, MaxBodySize)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			WriteError(w, http.StatusRequestEntityTooLarge, "request body too large")
			return false
		}
		WriteError(w, http.StatusBadRequest, "invalid request body")
		return false
	}
	// Reject trailing data after the first JSON object.
	if dec.More() {
		WriteError(w, http.StatusBadRequest, "invalid request body")
		return false
	}
	return true
}

// SetIfNotNil adds the dereferenced value to the map if the pointer is non-nil.
// Used by Update methods to build partial-update maps from optional request fields.
func SetIfNotNil[T any](m map[string]any, key string, val *T) {
	if val != nil {
		m[key] = *val
	}
}

func httpStatusToCode(status int) string {
	switch status {
	case http.StatusBadRequest:
		return "VALIDATION"
	case http.StatusUnauthorized:
		return "UNAUTHORIZED"
	case http.StatusForbidden:
		return "FORBIDDEN"
	case http.StatusNotFound:
		return "NOT_FOUND"
	case http.StatusConflict:
		return "CONFLICT"
	case http.StatusRequestEntityTooLarge:
		return "PAYLOAD_TOO_LARGE"
	case http.StatusUnprocessableEntity:
		return "UNPROCESSABLE_ENTITY"
	case http.StatusTooManyRequests:
		return "RATE_LIMITED"
	default:
		return "INTERNAL"
	}
}
