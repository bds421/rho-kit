package approval

import (
	"bytes"
	"context"
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

// Executor is the optional callback invoked when a request transitions
// from pending to approved. The middleware itself does not poll for
// state changes — services wire this from their approver endpoint
// (after [approval.Store.Decide] returns approved=true).
//
// Implementations should:
//   - Replay the original action using r.Payload as the body.
//   - Call [approval.Store.MarkExecuted] on success.
type Executor func(ctx context.Context, r approval.Request) error

// Option configures the middleware.
type Option func(*config)

type config struct {
	maxBodyBytes      int64
	expiry            time.Duration
	tenantSource      func(*http.Request) (string, bool)
	actorExtractor    func(*http.Request) string
	actionExtractor   func(*http.Request) string
	resourceExtractor func(*http.Request) string
	idFunc            func() string
	logger            *slog.Logger
	executor          Executor
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
// an approval without a tenant.
func WithTenantSource(fn func(*http.Request) (string, bool)) Option {
	return func(c *config) { c.tenantSource = fn }
}

// WithActorExtractor sets a function that returns the principal id for
// a request. The default returns "anonymous" — that is forensically
// useless, but failing 400 on every unauthenticated request would
// surprise callers who haven't yet wired auth. Most deployments wire
// this from JWT claims or a gRPC peer cert.
//
// Returning empty is treated as "anonymous"; the middleware never
// stores an empty actor because the data/approval store rejects it.
func WithActorExtractor(fn func(*http.Request) string) Option {
	return func(c *config) {
		c.actorExtractor = func(r *http.Request) string {
			v := fn(r)
			if v == "" {
				return "anonymous"
			}
			return v
		}
	}
}

// WithActionExtractor sets a function that returns the action verb for
// a request. Default: METHOD + " " + URL.Path, which is fine for
// administrative routes but coarse — services with verb-named routes
// (POST /v1/users/{id}/disable) should override to record "user.disable".
func WithActionExtractor(fn func(*http.Request) string) Option {
	return func(c *config) { c.actionExtractor = fn }
}

// WithResourceExtractor sets a function that returns the resource id
// for a request. Default: URL.Path. Override when the resource is a
// path parameter that needs lifting out (e.g. {id}).
func WithResourceExtractor(fn func(*http.Request) string) Option {
	return func(c *config) { c.resourceExtractor = fn }
}

// WithIDFunc overrides the approval id generator. Default: UUIDv7
// string.
func WithIDFunc(fn func() string) Option {
	return func(c *config) { c.idFunc = fn }
}

// WithLogger sets the slog.Logger for store-error reporting. Default:
// slog.Default().
func WithLogger(l *slog.Logger) Option {
	return func(c *config) { c.logger = l }
}

// WithExecutor wires a callback to run when a request transitions to
// approved. See [Executor] for the contract — the middleware itself
// does not invoke this; it's stored on the config so the approver
// endpoint can fetch it via [Middleware]'s closure if needed. Services
// typically wire the executor directly to their approver handler
// instead, but the option is here for symmetry with the spec.
func WithExecutor(fn Executor) Option {
	return func(c *config) { c.executor = fn }
}

func defaultConfig() config {
	return config{
		maxBodyBytes: DefaultMaxBodyBytes,
		expiry:       DefaultExpiry,
		tenantSource: func(r *http.Request) (string, bool) {
			v := r.Header.Get(DefaultTenantHeader)
			return v, v != ""
		},
		actorExtractor:    func(_ *http.Request) string { return "anonymous" },
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
// pending approval and responds 202 Accepted. Panics if store is nil.
func Middleware(store approval.Store, opts ...Option) func(http.Handler) http.Handler {
	if store == nil {
		panic("approval: Middleware requires a non-nil approval.Store")
	}
	cfg := defaultConfig()
	for _, o := range opts {
		o(&cfg)
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			tenantID, ok := cfg.tenantSource(r)
			if !ok || tenantID == "" {
				writeError(w, http.StatusBadRequest, "tenant id missing")
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
				Actor:     cfg.actorExtractor(r),
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
