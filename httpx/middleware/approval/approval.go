package approval

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/bds421/rho-kit/data/approval"
)

// DefaultMaxBodyBytes is the default cap on the size of a request body
// the middleware will persist into the approval store.
const DefaultMaxBodyBytes = 64 << 10 // 64 KiB

// DefaultExpiry is the default time after which a pending approval
// auto-rejects on the next Decide call.
const DefaultExpiry = 24 * time.Hour

// DefaultTenantHeader is the request header read for the tenant id when
// no override is configured. Services that already place tenant on the
// request context via earlier middleware should pass [WithTenantSource]
// instead.
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
	idFunc            func() string
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
// request. The default reads [DefaultTenantHeader]; services that put
// tenant on context via an upstream middleware should override.
//
// Returning ok=false produces 400 Bad Request — the kit cannot record
// an approval without a tenant. Panics on a nil fn.
func WithTenantSource(fn func(*http.Request) (string, bool)) Option {
	if fn == nil {
		panic("approval: WithTenantSource requires a non-nil function")
	}
	return func(c *config) { c.tenantSource = fn }
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

// WithActionExtractor sets a function that returns the action verb for
// a request. Default: METHOD + " " + URL.Path, which is fine for
// administrative routes but coarse — services with verb-named routes
// (POST /v1/users/{id}/disable) should override to record "user.disable".
// Panics on a nil fn.
func WithActionExtractor(fn func(*http.Request) string) Option {
	if fn == nil {
		panic("approval: WithActionExtractor requires a non-nil function")
	}
	return func(c *config) { c.actionExtractor = fn }
}

// WithResourceExtractor sets a function that returns the resource id
// for a request. Default: URL.Path. Override when the resource is a
// path parameter that needs lifting out (e.g. {id}). Panics on a nil
// fn.
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
	return func(c *config) { c.idFunc = fn }
}

// WithLogger sets the slog.Logger for store-error reporting. A nil
// logger is normalized back to [slog.Default] so test wiring stays
// ergonomic; the middleware never holds a nil logger.
func WithLogger(l *slog.Logger) Option {
	return func(c *config) {
		if l == nil {
			c.logger = slog.Default()
			return
		}
		c.logger = l
	}
}

func defaultConfig() config {
	return config{
		maxBodyBytes: DefaultMaxBodyBytes,
		expiry:       DefaultExpiry,
		tenantSource: func(r *http.Request) (string, bool) {
			v := r.Header.Get(DefaultTenantHeader)
			return v, v != ""
		},
		actionExtractor:   func(r *http.Request) string { return r.Method + " " + r.URL.Path },
		resourceExtractor: func(r *http.Request) string { return r.URL.Path },
		idFunc:            func() string { return uuid.Must(uuid.NewV7()).String() },
		logger:            slog.Default(),
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
		o(&cfg)
	}
	if cfg.actorExtractor == nil {
		panic("approval: Middleware requires WithActorExtractor; the kit will not default actors to anonymous on destructive operations")
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			tenantID, ok := cfg.tenantSource(r)
			if !ok || tenantID == "" {
				writeError(w, http.StatusBadRequest, "tenant id missing")
				return
			}

			actor, ok := cfg.actorExtractor(r)
			if !ok || actor == "" {
				writeError(w, http.StatusUnauthorized, "actor not resolved")
				return
			}

			body, err := readBody(r, cfg.maxBodyBytes)
			if errors.Is(err, errBodyTooLarge) {
				writeError(w, http.StatusRequestEntityTooLarge, "request body exceeds approval cap")
				return
			}
			if err != nil {
				cfg.logger.Error("approval: read body", "error", err)
				writeError(w, http.StatusBadRequest, "could not read request body")
				return
			}

			now := time.Now().UTC()
			req := approval.Request{
				ID:        cfg.idFunc(),
				TenantID:  tenantID,
				Actor:     actor,
				Action:    cfg.actionExtractor(r),
				Resource:  cfg.resourceExtractor(r),
				Payload:   body,
				CreatedAt: now,
				ExpiresAt: now.Add(cfg.expiry),
			}

			created, err := store.Create(r.Context(), req)
			if err != nil {
				cfg.logger.Error("approval: create", "error", err, "approval_id", req.ID)
				writeError(w, http.StatusInternalServerError, "could not record approval")
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
	defer r.Body.Close()
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

// writeError emits a small JSON error body. The middleware deliberately
// avoids depending on httpx's WriteError — this package is its own
// module so that consumers (the approver endpoint, integration tests)
// don't drag the full httpx dep just to wire approvals.
func writeError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// EnsureBodyBuffered returns an http.Request whose body is a re-readable
// bytes.Reader over body. Useful for executors that replay a stored
// payload through the original handler.
func EnsureBodyBuffered(r *http.Request, body []byte) *http.Request {
	r2 := r.Clone(r.Context())
	r2.Body = io.NopCloser(bytes.NewReader(body))
	r2.ContentLength = int64(len(body))
	return r2
}
