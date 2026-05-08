package httpx

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"log"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

// defaultMaxIdleConnsPerHost overrides the stdlib default of 2, which causes
// connection churn when a service makes many concurrent requests to a single
// downstream. 100 matches the typical service-to-service workload without
// being so large that misbehaving downstreams hog file descriptors.
const defaultMaxIdleConnsPerHost = 100

// ClientOption configures the kit-wide HTTP client transport.
type ClientOption func(*clientConfig)

type clientConfig struct {
	idleConnTimeout time.Duration
}

// WithIdleConnTimeout sets the maximum amount of time an idle (keep-alive)
// connection will remain idle before closing itself. The stdlib default
// (90s) outlives many production load balancers' idle-connection cap (often
// 60s on AWS ALB, 30s on Cloudflare), so the LB closes the conn first and
// the client retries against a half-closed socket.
//
// Default 0 keeps the stdlib's 90s. Set to under your LB's cap (typically
// 30s–60s) so the client closes first.
func WithIdleConnTimeout(d time.Duration) ClientOption {
	return func(c *clientConfig) { c.idleConnTimeout = d }
}

// newKitTransport clones http.DefaultTransport and applies kit-wide overrides.
// Processes that replace http.DefaultTransport with a custom RoundTripper
// (otelhttp wrappers, test doubles) cause the type assertion to fail; in that
// case fall back to a fresh *http.Transport with stdlib-style defaults so
// construction stays panic-free.
func newKitTransport(tlsConfig *tls.Config, cfg clientConfig) *http.Transport {
	var transport *http.Transport
	if tr, ok := http.DefaultTransport.(*http.Transport); ok {
		transport = tr.Clone()
	} else {
		transport = newFallbackTransport()
	}
	transport.MaxIdleConnsPerHost = defaultMaxIdleConnsPerHost
	if tlsConfig != nil {
		transport.TLSClientConfig = tlsConfig
	}
	// Enforce a TLS 1.2 floor on the outbound client. An empty
	// &tls.Config{} otherwise defaults to TLS 1.0, which is below the
	// modern compliance baseline. Caller-set higher floors are honored.
	if transport.TLSClientConfig == nil {
		transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	} else if transport.TLSClientConfig.MinVersion < tls.VersionTLS12 {
		transport.TLSClientConfig.MinVersion = tls.VersionTLS12
	}
	if cfg.idleConnTimeout > 0 {
		transport.IdleConnTimeout = cfg.idleConnTimeout
	}
	return transport
}

func newFallbackTransport() *http.Transport {
	return &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
}

// NewHTTPClient returns an *http.Client with the given timeout and optional TLS
// configuration. When tlsConfig is non-nil the client trusts the internal CA,
// ensuring all inter-service HTTPS calls work under mTLS.
// Use this instead of &http.Client{} to avoid accidentally creating a client
// that cannot verify the internal PKI certificates or that hits the stdlib's
// MaxIdleConnsPerHost=2 perf cliff.
//
// The transport is cloned from http.DefaultTransport to inherit production
// defaults (idle connection management, TLS handshake timeout, proxy support).
// Use [WithIdleConnTimeout] to override the stdlib 90s idle-connection cap
// when fronting a load balancer with a tighter idle timeout.
func NewHTTPClient(timeout time.Duration, tlsConfig *tls.Config, opts ...ClientOption) *http.Client {
	if timeout <= 0 {
		panic("httpx: NewHTTPClient requires a positive timeout — pass an explicit upper bound to avoid hung requests")
	}
	var cfg clientConfig
	for _, opt := range opts {
		opt(&cfg)
	}
	return &http.Client{
		Timeout:   timeout,
		Transport: newKitTransport(tlsConfig, cfg),
	}
}

// NewTracingHTTPClient returns an *http.Client instrumented with OpenTelemetry
// spans for outbound requests. It uses the same TLS setup as NewHTTPClient.
//
// otelhttp.Option values are passed through to the OTel transport wrapper;
// kit-level ClientOption values must be applied via the variadic ClientOption
// returned by [WithClientOptions].
func NewTracingHTTPClient(timeout time.Duration, tlsConfig *tls.Config, opts ...otelhttp.Option) *http.Client {
	if timeout <= 0 {
		panic("httpx: NewTracingHTTPClient requires a positive timeout — pass an explicit upper bound to avoid hung requests")
	}
	return &http.Client{
		Timeout:   timeout,
		Transport: otelhttp.NewTransport(newKitTransport(tlsConfig, clientConfig{}), opts...),
	}
}

// NewTracingHTTPClientWithOptions is the variant of [NewTracingHTTPClient]
// that accepts kit-level [ClientOption] values (e.g. [WithIdleConnTimeout])
// alongside the OTel transport options.
func NewTracingHTTPClientWithOptions(timeout time.Duration, tlsConfig *tls.Config, kitOpts []ClientOption, otelOpts ...otelhttp.Option) *http.Client {
	if timeout <= 0 {
		panic("httpx: NewTracingHTTPClientWithOptions requires a positive timeout — pass an explicit upper bound to avoid hung requests")
	}
	var cfg clientConfig
	for _, opt := range kitOpts {
		opt(&cfg)
	}
	return &http.Client{
		Timeout:   timeout,
		Transport: otelhttp.NewTransport(newKitTransport(tlsConfig, cfg), otelOpts...),
	}
}

// ServerOption customises the *http.Server returned by NewServer.
type ServerOption func(*http.Server)

// WithWriteTimeout overrides the default write timeout.
// Use 0 for WebSocket servers where pumps manage their own deadlines.
//
// CONSTRAINT: WriteTimeout must exceed any per-request middleware timeout
// (typically `stack.WithTimeout`) by enough margin to let the middleware
// write its 503 timeout response. The kit's `Default` stack uses 30s
// for the middleware and the server defaults to 35s, leaving 5s of
// margin. If you raise the middleware timeout, raise this in lockstep.
func WithWriteTimeout(d time.Duration) ServerOption {
	return func(s *http.Server) { s.WriteTimeout = d }
}

// WithTLSConfig sets the server TLS configuration for mTLS.
// When set, lifecycle.HTTPServer uses ListenAndServeTLS instead of ListenAndServe.
func WithTLSConfig(cfg *tls.Config) ServerOption {
	return func(s *http.Server) { s.TLSConfig = cfg }
}

// WithErrorLog sets the logger used by net/http for protocol errors (TLS
// handshake failures, connection-level read errors). When unset, NewServer
// routes ErrorLog through slog so client RemoteAddrs and other connection
// errors land in structured logs instead of plain stdout via the global
// "log" package.
func WithErrorLog(l *log.Logger) ServerOption {
	return func(s *http.Server) { s.ErrorLog = l }
}

// NewServer returns an *http.Server with safe production defaults.
// Options may override individual fields.
//
// ErrorLog defaults to a slog-backed adapter so net/http's connection-level
// error messages (TLS handshake failures, peer-reset reads) flow through the
// structured logger rather than the global "log" package — without this,
// raw client RemoteAddrs leak to stdout and pollute SIEMs.
func NewServer(addr string, handler http.Handler, opts ...ServerOption) *http.Server {
	if handler == nil {
		panic("httpx: NewServer requires a non-nil handler — net/http would otherwise serve http.DefaultServeMux and expose globally-registered handlers")
	}
	srv := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadTimeout:       15 * time.Second,
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      35 * time.Second, // Must exceed the configured request timeout so middleware can write 503
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20, // 1 MB
		ErrorLog:          slog.NewLogLogger(slog.Default().Handler(), slog.LevelWarn),
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
	// Reject trailing data after the first JSON object. dec.More() only
	// detects continuation within an array/object stream, not a second
	// top-level value: bodies like `{"a":1} {"b":2}` slip past it. The
	// reliable check is to attempt one more decode and require io.EOF —
	// anything else means trailing content.
	var trailing json.RawMessage
	if err := dec.Decode(&trailing); !errors.Is(err, io.EOF) {
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
	case http.StatusBadGateway:
		return "BAD_GATEWAY"
	case http.StatusServiceUnavailable:
		return "SERVICE_UNAVAILABLE"
	default:
		return "INTERNAL"
	}
}
