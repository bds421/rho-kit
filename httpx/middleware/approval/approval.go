package approval

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strconv"
	"time"

	"github.com/bds421/rho-kit/core/v2/id"
	"github.com/bds421/rho-kit/core/v2/redact"
	coretenant "github.com/bds421/rho-kit/core/v2/tenant"
	"golang.org/x/net/http/httpguts"

	"github.com/bds421/rho-kit/data/v2/approval"
	"github.com/bds421/rho-kit/httpx/v2"
	"github.com/bds421/rho-kit/httpx/v2/internal/headerutil"
)

// DefaultMaxBodyBytes is the default cap on the size of a request body
// the middleware will persist into the approval store.
const DefaultMaxBodyBytes = 64 << 10 // 64 KiB

// DefaultExpiry is the default time after which a pending approval
// auto-rejects on the next Decide call.
const DefaultExpiry = 24 * time.Hour

// DefaultTenantHeader is the conventional request header for the
// tenant id. v2 no longer trusts this header by default — call
// [WithTenantFromHeader] explicitly when the service runs behind a
// proxy that strips and re-stamps the header from a verified
// identity. The constant is exported so callers wiring
// [WithTenantFromHeader] do not have to invent their own string.
const DefaultTenantHeader = "X-Tenant-ID"

// Option configures the middleware.
type Option func(*config)

type config struct {
	maxBodyBytes      int64
	expiry            time.Duration
	tenantSource      func(*http.Request) (string, bool)
	actorExtractor    func(*http.Request) (string, bool)
	actionExtractor   func(*http.Request) string
	resourceExtractor func(*http.Request) string
	idFunc            func() (string, error)
	logger            *slog.Logger
}

// WithMaxBodyBytes overrides the body size cap. Panics on non-positive
// values — a zero cap would persist empty bodies only, which is
// almost always a misconfiguration.
func WithMaxBodyBytes(n int64) Option {
	if n <= 0 {
		panic("approval: WithMaxBodyBytes requires a positive value")
	}
	return func(c *config) { c.maxBodyBytes = n }
}

// WithExpiry sets the time-to-live for pending requests. Panics on
// non-positive durations — a zero/negative TTL produces requests that
// are auto-rejected on creation.
func WithExpiry(d time.Duration) Option {
	if d <= 0 {
		panic("approval: WithExpiry requires a positive duration")
	}
	return func(c *config) { c.expiry = d }
}

// WithTenantSource sets the function that resolves a tenant id for a
// request. The default reads the authenticated tenant from request
// context via [coretenant.FromContext] — services that have not
// installed an auth middleware that populates that context MUST
// provide an explicit override (or opt into [WithTenantFromHeader]
// after auditing the trust boundary).
//
// Returning ok=false produces 400 Bad Request — the kit cannot record
// an approval without a tenant. Panics on a nil fn.
func WithTenantSource(fn func(*http.Request) (string, bool)) Option {
	if fn == nil {
		panic("approval: WithTenantSource requires a non-nil function")
	}
	return func(c *config) { c.tenantSource = fn }
}

// WithTenantFromHeader is the EXPLICIT opt-in that lets the middleware
// read the tenant id from a caller-controlled HTTP header.
//
// SECURITY: any HTTP client able to reach this endpoint can set the
// header to an arbitrary value, which would let an unauthenticated
// caller submit an approval request under a victim tenant. Use this
// option ONLY when the service is exclusively reachable through a
// trusted proxy that strips inbound copies of the header and re-stamps
// it from a verified identity (e.g. an ingress that pins
// X-Tenant-ID from a verified JWT claim and rejects requests that
// also carry the header from the public Internet).
//
// In every other deployment, prefer the default ctx-based source
// populated by an auth middleware. The default in v2 no longer
// silently trusts the header — pre-v2 callers that relied on the
// implicit header trust MUST add this option after auditing.
//
// The header must be a singleton token: duplicate lines,
// comma-combined values, whitespace, and control characters are
// rejected before the value is validated as a tenant ID.
func WithTenantFromHeader(header string) Option {
	return WithTenantSource(tenantSourceFromHeader(header))
}

// WithActorExtractor sets a function that resolves the principal id
// for a request. REQUIRED — [Middleware] panics at construction if no
// extractor is configured. Returning ok=false (or an empty actor)
// causes the middleware to respond 401 Unauthorized: the kit will not
// record an "anonymous" actor on a destructive operation, since that
// strips forensics value at the moment it matters most.
//
// Most deployments wire this from JWT claims or a gRPC peer cert.
// Panics on a nil fn.
func WithActorExtractor(fn func(*http.Request) (string, bool)) Option {
	if fn == nil {
		panic("approval: WithActorExtractor requires a non-nil function")
	}
	return func(c *config) { c.actorExtractor = fn }
}

// WithActorFromHeader sets the actor extractor to a request header.
//
// SECURITY WARNING: any caller able to reach this service can set the header to
// an arbitrary value. Use this only when the service is exclusively reachable
// through a trusted proxy or middleware that strips inbound values and re-stamps
// the header from a verified identity. In normal services prefer extracting the
// actor from authenticated context populated by auth middleware.
//
// The header must be a singleton identity token: duplicate lines,
// comma-combined values, whitespace, and control characters are rejected.
func WithActorFromHeader(header string) Option {
	if !httpguts.ValidHeaderFieldName(header) {
		panic("approval: WithActorFromHeader requires a valid non-empty header name")
	}
	return WithActorExtractor(func(r *http.Request) (string, bool) {
		return headerutil.SingletonIdentity(r.Header, header)
	})
}

// WithActionExtractor sets a function that returns the action verb for
// a request. Default: METHOD + " " + URL.EscapedPath(), which keeps
// encoded path delimiters distinguishable but is coarse for administrative
// routes — services with verb-named routes (POST /v1/users/{id}/disable)
// should override to record "user.disable". Panics on a nil fn.
func WithActionExtractor(fn func(*http.Request) string) Option {
	if fn == nil {
		panic("approval: WithActionExtractor requires a non-nil function")
	}
	return func(c *config) { c.actionExtractor = fn }
}

// WithResourceExtractor sets a function that returns the resource id for a
// request. Default: URL.EscapedPath(). Override when the resource is a path
// parameter that needs lifting out (e.g. {id}). Panics on a nil fn.
func WithResourceExtractor(fn func(*http.Request) string) Option {
	if fn == nil {
		panic("approval: WithResourceExtractor requires a non-nil function")
	}
	return func(c *config) { c.resourceExtractor = fn }
}

// WithIDFunc overrides the approval id generator. Default: UUIDv7
// string. Panics on a nil fn.
func WithIDFunc(fn func() string) Option {
	if fn == nil {
		panic("approval: WithIDFunc requires a non-nil function")
	}
	return func(c *config) {
		c.idFunc = func() (string, error) {
			return fn(), nil
		}
	}
}

// WithIDFuncE overrides the approval id generator with an error-returning
// function. Default: UUIDv7 string. Panics on a nil fn.
func WithIDFuncE(fn func() (string, error)) Option {
	if fn == nil {
		panic("approval: WithIDFuncE requires a non-nil function")
	}
	return func(c *config) { c.idFunc = fn }
}

// WithLogger sets the slog.Logger for store-error reporting.
// Panics if l is nil — omit the option to keep slog.Default(), matching
// the kit's dominant middleware WithLogger contract.
func WithLogger(l *slog.Logger) Option {
	if l == nil {
		panic("middleware/approval: WithLogger requires a non-nil logger (omit the option to use slog.Default)")
	}
	return func(c *config) { c.logger = l }
}

func defaultConfig() config {
	return config{
		maxBodyBytes:      DefaultMaxBodyBytes,
		expiry:            DefaultExpiry,
		tenantSource:      tenantFromContext,
		actionExtractor:   func(r *http.Request) string { return r.Method + " " + httpx.RequestPath(r) },
		resourceExtractor: httpx.RequestPath,
		idFunc: func() (string, error) {
			return id.New(), nil
		},
		logger: slog.Default(),
	}
}

// tenantFromContext is the secure default for tenant resolution: it
// reads the tenant id from request context, where an upstream auth
// middleware has placed it from a verified claim. Returning ok=false
// here results in 400 Bad Request — callers that legitimately need to
// run without auth context must opt in to [WithTenantFromHeader] or
// supply a custom [WithTenantSource].
func tenantFromContext(r *http.Request) (string, bool) {
	if r == nil {
		return "", false
	}
	id, ok := coretenant.FromContext(r.Context())
	if !ok {
		return "", false
	}
	return id.String(), true
}

func tenantSourceFromHeader(header string) func(*http.Request) (string, bool) {
	if !httpguts.ValidHeaderFieldName(header) {
		panic("approval: WithTenantFromHeader requires a valid non-empty header name")
	}
	return func(r *http.Request) (string, bool) {
		value, present, ok := headerutil.SingletonToken(r.Header, header)
		if !present || !ok {
			return "", false
		}
		id, err := coretenant.NewID(value)
		if err != nil {
			return "", false
		}
		return id.String(), true
	}
}

// Response is the JSON shape returned to the caller on a successful
// 202 Accepted.
type Response struct {
	ApprovalID string `json:"approval_id"`
	Status     string `json:"status"`
}

// Middleware returns http middleware that records the request as a
// pending approval and responds 202 Accepted. Panics if store is nil
// or if no [WithActorExtractor] is configured — a missing extractor
// would otherwise silently record "anonymous" against destructive
// operations, which the kit refuses to do.
func Middleware(store approval.Store, opts ...Option) func(http.Handler) http.Handler {
	if store == nil {
		panic("approval: Middleware requires a non-nil approval.Store")
	}
	cfg := defaultConfig()
	for _, o := range opts {
		if o == nil {
			panic("approval: Middleware option must not be nil")
		}
		o(&cfg)
	}
	if cfg.actorExtractor == nil {
		panic("approval: Middleware requires WithActorExtractor; the kit will not default actors to anonymous on destructive operations")
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			tenantID, ok := safeTenantSource(cfg.logger, cfg.tenantSource, r)
			if !ok || tenantID == "" {
				httpx.WriteError(w, http.StatusBadRequest, "tenant id missing")
				return
			}

			actor, ok := safeActorExtractor(cfg.logger, cfg.actorExtractor, r)
			if !ok || actor == "" {
				httpx.WriteError(w, http.StatusUnauthorized, "actor not resolved")
				return
			}

			body, err := readBody(r, cfg.maxBodyBytes)
			if errors.Is(err, errBodyTooLarge) {
				httpx.WriteError(w, http.StatusRequestEntityTooLarge, "request body exceeds approval cap")
				return
			}
			if err != nil {
				cfg.logger.Error("approval: read body", redact.Error(err))
				httpx.WriteError(w, http.StatusBadRequest, "could not read request body")
				return
			}

			id, ok := safeIDFunc(cfg.logger, cfg.idFunc)
			if !ok {
				httpx.WriteError(w, http.StatusInternalServerError, "could not record approval")
				return
			}
			action, ok := safeStringExtractor(cfg.logger, "action extractor", cfg.actionExtractor, r)
			if !ok {
				httpx.WriteError(w, http.StatusInternalServerError, "could not record approval")
				return
			}
			resource, ok := safeStringExtractor(cfg.logger, "resource extractor", cfg.resourceExtractor, r)
			if !ok {
				httpx.WriteError(w, http.StatusInternalServerError, "could not record approval")
				return
			}

			now := time.Now().UTC()
			req := approval.Request{
				ID:        id,
				TenantID:  tenantID,
				Actor:     actor,
				Action:    action,
				Resource:  resource,
				Payload:   body,
				CreatedAt: now,
				ExpiresAt: now.Add(cfg.expiry),
			}

			created, err := store.Create(r.Context(), req)
			if err != nil {
				cfg.logger.Error("approval: create", redact.Error(err), redact.String("approval_id", req.ID))
				httpx.WriteError(w, http.StatusInternalServerError, "could not record approval")
				return
			}

			// next intentionally unused on the pending path — the
			// downstream handler runs only when an executor replays
			// the request after a Decide=approved. Keep the parameter
			// in the closure so the option to wire a non-blocking
			// pre-flight (e.g. a fast schema check) stays reachable
			// via composition without a breaking signature change.
			_ = next

			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(Response{
				ApprovalID: created.ID,
				Status:     string(created.State),
			})
		})
	}
}

func safeTenantSource(logger *slog.Logger, fn func(*http.Request) (string, bool), r *http.Request) (tenantID string, ok bool) {
	defer func() {
		if rec := recover(); rec != nil {
			logCallbackPanic(logger, "tenant source", rec)
			tenantID, ok = "", false
		}
	}()
	return fn(r)
}

func safeActorExtractor(logger *slog.Logger, fn func(*http.Request) (string, bool), r *http.Request) (actor string, ok bool) {
	defer func() {
		if rec := recover(); rec != nil {
			logCallbackPanic(logger, "actor extractor", rec)
			actor, ok = "", false
		}
	}()
	return fn(r)
}

func safeStringExtractor(logger *slog.Logger, callback string, fn func(*http.Request) string, r *http.Request) (value string, ok bool) {
	defer func() {
		if rec := recover(); rec != nil {
			logCallbackPanic(logger, callback, rec)
			value, ok = "", false
		}
	}()
	return fn(r), true
}

func safeIDFunc(logger *slog.Logger, fn func() (string, error)) (id string, ok bool) {
	defer func() {
		if rec := recover(); rec != nil {
			logCallbackPanic(logger, "id function", rec)
			id, ok = "", false
		}
	}()
	id, err := fn()
	if err != nil {
		if logger == nil {
			logger = slog.Default()
		}
		logger.Error("approval: id function failed", redact.Error(err))
		return "", false
	}
	return id, true
}

func logCallbackPanic(logger *slog.Logger, callback string, rec any) {
	if logger == nil {
		logger = slog.Default()
	}
	logger.Error("approval: callback panicked",
		"callback", callback,
		redact.Panic(rec),
		"stack", string(debug.Stack()),
	)
}

// errBodyTooLarge is returned by readBody when the request body exceeds
// the configured cap.
var errBodyTooLarge = errors.New("approval: body too large")

// readBody reads up to max+1 bytes from r.Body. If the body has more
// than max bytes, returns errBodyTooLarge — we read max+1 specifically
// to detect the boundary case where len == max+1 (still too large) vs
// len == max (acceptable).
func readBody(r *http.Request, max int64) ([]byte, error) {
	if r.Body == nil {
		return nil, nil
	}
	defer func() { _ = r.Body.Close() }()
	limited := io.LimitReader(r.Body, max+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > max {
		return nil, errBodyTooLarge
	}
	if len(body) == 0 {
		return nil, nil
	}
	return body, nil
}

// EnsureBodyBuffered returns an http.Request whose body is a re-readable
// bytes.Reader over a detached copy of body. Useful for executors that replay
// a stored payload through the original handler or an http.Client.
//
// r.Clone copies GetBody and the Header map from the source request. Both are
// rewired here so a replay stays consistent with the buffered payload: GetBody
// returns a fresh reader over the owned copy (so the http.Transport reconstructs
// the buffered body on retry/redirect rather than resurrecting the original),
// and any stale Content-Length header is overwritten to match.
func EnsureBodyBuffered(r *http.Request, body []byte) *http.Request {
	r2 := r.Clone(r.Context())
	owned := append([]byte(nil), body...)
	r2.Body = io.NopCloser(bytes.NewReader(owned))
	r2.ContentLength = int64(len(owned))
	r2.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(owned)), nil
	}
	r2.Header.Set("Content-Length", strconv.FormatInt(int64(len(owned)), 10))
	return r2
}
