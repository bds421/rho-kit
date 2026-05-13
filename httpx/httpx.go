package httpx

import (
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"log"
	"log/slog"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/bds421/rho-kit/core/v2/apperror"
	"github.com/bds421/rho-kit/core/v2/redact"
	"github.com/bds421/rho-kit/httpx/v2/internal/transportdefaults"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"golang.org/x/net/http/httpguts"
)

// defaultMaxIdleConnsPerHost overrides the stdlib default of 2, which causes
// connection churn when a service makes many concurrent requests to a single
// downstream. 100 matches the typical service-to-service workload without
// being so large that misbehaving downstreams hog file descriptors.
const defaultMaxIdleConnsPerHost = transportdefaults.DefaultMaxIdleConnsPerHost

const minimumTLSVersion = transportdefaults.MinimumTLSVersion

// ClientOption configures the kit-wide HTTP client transport.
type ClientOption func(*clientConfig)

type clientConfig struct {
	idleConnTimeout time.Duration
	checkRedirect   func(*http.Request, []*http.Request) error
}

// ErrRedirectBlocked is returned by kit-created HTTP clients when a response
// attempts to redirect but redirect following was not explicitly enabled.
var ErrRedirectBlocked = errors.New("httpx: redirects are disabled by default")

// ErrRedirectLimitExceeded is returned when an explicitly-enabled redirect
// chain exceeds the configured hop limit.
var ErrRedirectLimitExceeded = errors.New("httpx: redirect limit exceeded")

// WithIdleConnTimeout sets the maximum amount of time an idle (keep-alive)
// connection will remain idle before closing itself. The stdlib default
// (90s) outlives many production load balancers' idle-connection cap (often
// 60s on AWS ALB, 30s on Cloudflare), so the LB closes the conn first and
// the client retries against a half-closed socket.
//
// Default 0 keeps the stdlib's 90s. Set to under your LB's cap (typically
// 30s–60s) so the client closes first.
func WithIdleConnTimeout(d time.Duration) ClientOption {
	if d < 0 {
		panic("httpx: WithIdleConnTimeout requires a non-negative duration")
	}
	return func(c *clientConfig) { c.idleConnTimeout = d }
}

// WithFollowRedirects enables bounded redirect following for kit-created HTTP
// clients. By default redirects are blocked with [ErrRedirectBlocked], which
// avoids surprising cross-host requests from internal service clients.
func WithFollowRedirects(maxHops int) ClientOption {
	if maxHops <= 0 {
		panic("httpx: WithFollowRedirects requires maxHops > 0")
	}
	return func(c *clientConfig) { c.checkRedirect = boundedRedirectPolicy(maxHops) }
}

func redirectPolicyOrDefault(policy func(*http.Request, []*http.Request) error) func(*http.Request, []*http.Request) error {
	if policy != nil {
		return policy
	}
	return blockRedirect
}

func blockRedirect(_ *http.Request, _ []*http.Request) error {
	return ErrRedirectBlocked
}

func boundedRedirectPolicy(maxHops int) func(*http.Request, []*http.Request) error {
	return func(_ *http.Request, via []*http.Request) error {
		if len(via) > maxHops {
			return ErrRedirectLimitExceeded
		}
		return nil
	}
}

// newKitTransport clones http.DefaultTransport and applies kit-wide overrides.
// Processes that replace http.DefaultTransport with a custom RoundTripper
// (otelhttp wrappers, test doubles) cause the type assertion to fail; in that
// case fall back to a fresh *http.Transport with stdlib-style defaults so
// construction stays panic-free.
func cloneTLSConfigWithFloor(cfg *tls.Config, label string) *tls.Config {
	return transportdefaults.CloneTLSConfigWithFloor(cfg, label)
}

func newKitTransport(tlsConfig *tls.Config, cfg clientConfig) *http.Transport {
	return newKitTransportWithLabel(tlsConfig, cfg, "httpx: NewHTTPClient")
}

func newKitTransportWithLabel(tlsConfig *tls.Config, cfg clientConfig, label string) *http.Transport {
	return transportdefaults.New(tlsConfig, cfg.idleConnTimeout, label)
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
		if opt == nil {
			panic("httpx: NewHTTPClient option must not be nil")
		}
		opt(&cfg)
	}
	return &http.Client{
		Timeout:       timeout,
		Transport:     newKitTransport(tlsConfig, cfg),
		CheckRedirect: redirectPolicyOrDefault(cfg.checkRedirect),
	}
}

// TracingClientOption configures the kit-tracing HTTP client. It accepts
// either a kit-level [ClientOption] (e.g. [WithIdleConnTimeout]) via
// [WithKitOption], or an OpenTelemetry transport option via [WithOTel].
type TracingClientOption func(*tracingClientConfig)

type tracingClientConfig struct {
	kit  clientConfig
	otel []otelhttp.Option
}

// WithKitOption wraps a kit [ClientOption] so it can be passed to
// [NewTracingHTTPClient].
func WithKitOption(opt ClientOption) TracingClientOption {
	if opt == nil {
		panic("httpx: WithKitOption requires a non-nil ClientOption")
	}
	return func(c *tracingClientConfig) { opt(&c.kit) }
}

// WithOTel wraps an [otelhttp.Option] so it can be passed to
// [NewTracingHTTPClient].
func WithOTel(opt otelhttp.Option) TracingClientOption {
	if opt == nil {
		panic("httpx: WithOTel requires a non-nil otelhttp.Option")
	}
	return func(c *tracingClientConfig) { c.otel = append(c.otel, opt) }
}

// NewTracingHTTPClient returns an *http.Client instrumented with OpenTelemetry
// spans for outbound requests. It uses the same TLS setup as NewHTTPClient.
//
// Redirects are blocked by default. Pass [WithKitOption] paired with
// [WithFollowRedirects] when a bounded redirect chain is intentional. Kit
// options ([WithIdleConnTimeout], …) are forwarded via [WithKitOption], and
// OpenTelemetry transport options via [WithOTel].
func NewTracingHTTPClient(timeout time.Duration, tlsConfig *tls.Config, opts ...TracingClientOption) *http.Client {
	if timeout <= 0 {
		panic("httpx: NewTracingHTTPClient requires a positive timeout — pass an explicit upper bound to avoid hung requests")
	}
	var cfg tracingClientConfig
	for _, opt := range opts {
		if opt == nil {
			panic("httpx: NewTracingHTTPClient option must not be nil")
		}
		opt(&cfg)
	}
	return &http.Client{
		Timeout:       timeout,
		Transport:     otelhttp.NewTransport(newKitTransportWithLabel(tlsConfig, cfg.kit, "httpx: NewTracingHTTPClient"), cfg.otel...),
		CheckRedirect: redirectPolicyOrDefault(cfg.kit.checkRedirect),
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
	if d < 0 {
		panic("httpx: WithWriteTimeout requires a non-negative duration")
	}
	return func(s *http.Server) { s.WriteTimeout = d }
}

// WithTLSConfig sets the server TLS configuration for mTLS. The config is
// cloned and normalized to the kit TLS floor before installation.
// When set, lifecycle.HTTPServer uses ListenAndServeTLS instead of ListenAndServe.
func WithTLSConfig(cfg *tls.Config) ServerOption {
	if cfg == nil {
		panic("httpx: WithTLSConfig requires a non-nil tls.Config")
	}
	owned := cloneTLSConfigWithFloor(cfg, "httpx: WithTLSConfig")
	return func(s *http.Server) {
		s.TLSConfig = cloneTLSConfigWithFloor(owned, "httpx: WithTLSConfig")
	}
}

// WithErrorLog sets the logger used by net/http for protocol errors (TLS
// handshake failures, connection-level read errors). When unset, NewServer
// routes ErrorLog through slog so client RemoteAddrs and other connection
// errors land in structured logs instead of plain stdout via the global
// "log" package.
func WithErrorLog(l *log.Logger) ServerOption {
	if l == nil {
		panic("httpx: WithErrorLog requires a non-nil logger")
	}
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
	if addr == "" {
		panic("httpx: NewServer requires a non-empty addr")
	}
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
		if opt == nil {
			panic("httpx: NewServer option must not be nil")
		}
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

// WriteJSON writes a JSON response with the given status code, using the
// request-scoped logger from r.Context for write-failure reporting. If
// JSON marshalling fails, it returns a 500 with a safe error body.
//
// Error responses (4xx/5xx) automatically include Cache-Control: no-store to
// prevent CDNs or browsers from caching error states. Success responses do not
// set Cache-Control, allowing callers to set appropriate caching headers.
//
// The response is fully buffered before writing to allow correct error handling:
// if marshalling fails, a 500 can still be sent since no bytes have been written.
// For large paginated responses consider a streaming variant.
//
// Returns the first error encountered (marshal failure or socket write
// failure). The error is also logged at Warn level via the request-scoped
// logger; most handlers can ignore the return value.
//
// r may be nil in tests or for handlers that have no request to scope on;
// in that case [slog.Default] is used for write-failure logging.
func WriteJSON(w http.ResponseWriter, r *http.Request, status int, v any) error {
	logger := slog.Default()
	if r != nil {
		logger = Logger(r.Context(), logger)
	}
	w.Header().Set("Content-Type", "application/json")
	if status >= 400 {
		w.Header().Set("Cache-Control", "no-store")
	}
	buf, err := json.Marshal(v)
	if err != nil {
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"internal error","code":"INTERNAL"}` + "\n"))
		return err
	}
	w.WriteHeader(status)
	if _, err = w.Write(buf); err != nil {
		logger.Warn("httpx: response write failed", redact.Error(err))
		return err
	}
	if _, err = w.Write([]byte("\n")); err != nil {
		logger.Warn("httpx: response write failed", redact.Error(err))
		return err
	}
	return nil
}

// WriteError writes a JSON error response with a machine-readable code
// derived from the HTTP status. Errors during the underlying write are
// logged via the request-scoped logger; the result is discarded so handlers
// that don't care about delivery can call this without ceremony.
func WriteError(w http.ResponseWriter, status int, msg string) {
	_ = WriteJSON(w, nil, status, APIError{Error: msg, Code: httpStatusToCode(status)})
}

// ParseID extracts a uint "id" path parameter from the request.
// Returns (0, false) for ID 0 since auto-increment databases never use it.
func ParseID(r *http.Request) (uint, bool) {
	if r == nil {
		return 0, false
	}
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
// The request must carry exactly one JSON Content-Type header (application/json
// or a structured +json media type). This keeps the JSON boundary safe even
// when callers mount typed handlers without the full default middleware stack.
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
	if r == nil || r.Body == nil {
		WriteError(w, http.StatusBadRequest, "invalid request body")
		return false
	}
	if values := r.Header.Values("Content-Type"); len(values) != 1 || !IsJSONContentType(values[0]) {
		WriteError(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
		return false
	}

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

// IsJSONContentType reports whether value is a JSON media type.
//
// Accepts application/json and structured syntax suffixes such as
// application/problem+json and application/merge-patch+json. Parameters
// like charset are allowed.
func IsJSONContentType(value string) bool {
	if value == "" || !utf8.ValidString(value) || !httpguts.ValidHeaderFieldValue(value) {
		return false
	}
	mediaType, _, err := mime.ParseMediaType(value)
	if err != nil {
		return false
	}
	mediaType = strings.ToLower(mediaType)
	return mediaType == "application/json" || strings.HasSuffix(mediaType, "+json")
}

func httpStatusToCode(status int) string {
	switch status {
	case http.StatusBadRequest:
		return string(apperror.CodeValidation)
	case http.StatusUnauthorized:
		return string(apperror.CodeAuthRequired)
	case http.StatusForbidden:
		return string(apperror.CodeForbidden)
	case http.StatusNotFound:
		return string(apperror.CodeNotFound)
	case http.StatusMethodNotAllowed:
		return "METHOD_NOT_ALLOWED"
	case http.StatusConflict:
		return string(apperror.CodeConflict)
	case http.StatusRequestTimeout:
		return "REQUEST_TIMEOUT"
	case http.StatusRequestEntityTooLarge:
		return "PAYLOAD_TOO_LARGE"
	case http.StatusUnsupportedMediaType:
		return "UNSUPPORTED_MEDIA_TYPE"
	case http.StatusUnprocessableEntity:
		return string(apperror.CodePermanent)
	case http.StatusTooManyRequests:
		return string(apperror.CodeRateLimit)
	case http.StatusBadGateway:
		return "BAD_GATEWAY"
	case http.StatusServiceUnavailable:
		return string(apperror.CodeUnavailable)
	case http.StatusInsufficientStorage:
		return string(apperror.CodeStorageFull)
	default:
		return "INTERNAL"
	}
}
