// Package budget provides an [http.RoundTripper] that charges a
// per-key cost budget on every outbound request.
//
// The wrapper is the outbound twin of httpx/middleware/budget: that
// guards inbound endpoints against tenants exceeding their cap;
// this guards outbound calls (typically to LLM / embedding /
// external-API providers) so a tenant's spend on the upstream is
// debited from the same per-period budget.
//
// # Wire shape
//
// Each request charges a fixed estimate, then optionally reconciles
// the actual cost from a response header:
//
//  1. Before sending, charge `1` (or the integer in EstimateHeader
//     if the caller filled it in, e.g. X-Estimated-Tokens).
//  2. After the upstream returns, if the response carries
//     ActualHeader (e.g. X-Actual-Tokens), refund the over-estimate
//     or charge the under-estimate as a delta.
//
// # Enforcement
//
// The default enforcement mode ([EnforcementHard]) rejects the
// response when the upstream's actual cost exceeds the pre-charged
// estimate and the delta cannot be charged against the remaining
// budget. The response body is closed and [ErrBudgetExceeded] is
// returned so callers cannot consume bytes the budget did not
// authorize. [EnforcementAuditOnly] preserves the historical
// best-effort behavior: failed delta charges are logged but the
// response is still returned.
//
// Transport errors retain the pre-charge by default. A timeout, broken pipe,
// or response-header error can happen after the upstream has already performed
// paid work, so automatic refunds are not a safe spend-control default. Use
// [WithRefundOnTransportError] only for upstreams that guarantee failed
// requests are never charged.
//
// # Sentinel error
//
// When the budget rejects the pre-charge or a hard-enforced delta
// charge, [ErrBudgetExceeded] is returned. Callers can
// `errors.Is(err, budget.ErrBudgetExceeded)` to distinguish "we said
// no" from upstream HTTP 429s.
package budget

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/bds421/rho-kit/core/v2/redact"
	"github.com/bds421/rho-kit/data/v2/budget"
	"github.com/bds421/rho-kit/httpx/v2/internal/transportdefaults"
	"golang.org/x/net/http/httpguts"
)

// ErrBudgetExceeded is the sentinel returned by RoundTrip when the
// pre-charge fails or, under [EnforcementHard], when the actual-cost
// delta cannot be charged. Callers compare with [errors.Is].
var ErrBudgetExceeded = errors.New("httpx/budget: budget exceeded")

// ErrInvalidRequest is returned when the transport is asked to process a
// structurally invalid request.
var ErrInvalidRequest = errors.New("httpx/budget: invalid request")

// Logger is the minimal interface accepted by [WithLogger]. *slog.Logger
// satisfies it.
type Logger interface {
	Warn(msg string, args ...any)
}

// Enforcement selects how actual-cost reconciliation reacts to a
// delta the budget cannot pay.
type Enforcement int

const (
	// EnforcementHard rejects the upstream response when the actual-cost
	// delta cannot be charged. The body is closed and [ErrBudgetExceeded]
	// is returned. This is the default.
	EnforcementHard Enforcement = iota
	// EnforcementAuditOnly logs delta failures but still returns the
	// response. Use only when reconciliation is for accounting and the
	// caller has out-of-band quota controls.
	EnforcementAuditOnly
)

// defaultCleanupTimeout bounds the post-response refund and
// reconcile path so a slow budget backend cannot stall the
// transport indefinitely.
const defaultCleanupTimeout = 2 * time.Second

// Option configures the [Wrap]ed RoundTripper.
type Option func(*config)

type config struct {
	estimateHeader string
	actualHeader   string
	defaultAmount  int64
	logger         Logger
	enforcement    Enforcement
	cleanupTimeout time.Duration
	refundOnError  bool
}

// WithEstimateHeader names a request header whose integer value is
// charged instead of the default. Use this when callers can compute
// a per-request cost upstream (e.g. tokenising the prompt before
// dispatch). When the header is absent or unparseable the default
// amount is charged instead.
func WithEstimateHeader(name string) Option {
	if name != "" && !httpguts.ValidHeaderFieldName(name) {
		panic("httpx/budget: WithEstimateHeader requires a valid HTTP header field name")
	}
	return func(c *config) { c.estimateHeader = name }
}

// WithActualHeader names a response header whose integer value is
// the authoritative cost. The wrapper reconciles the difference
// between the estimate and the actual after the upstream returns.
// Set "" to disable reconciliation.
func WithActualHeader(name string) Option {
	if name != "" && !httpguts.ValidHeaderFieldName(name) {
		panic("httpx/budget: WithActualHeader requires a valid HTTP header field name")
	}
	return func(c *config) { c.actualHeader = name }
}

// WithDefaultAmount sets the per-request charge when no estimate
// header is set. Default: 1.
func WithDefaultAmount(n int64) Option {
	if n < 0 {
		panic("httpx/budget: WithDefaultAmount requires a non-negative amount")
	}
	return func(c *config) { c.defaultAmount = n }
}

// WithLogger sets the logger used for non-fatal reconciliation
// warnings. Passing nil falls back to slog.Default(); the kit-wide
// convention is that loggers normalize nil rather than panic.
func WithLogger(l Logger) Option {
	return func(c *config) {
		if l == nil {
			c.logger = slog.Default()
			return
		}
		c.logger = l
	}
}

// WithEnforcement selects how the wrapper reacts when the actual-cost
// delta cannot be charged. Default: [EnforcementHard].
func WithEnforcement(e Enforcement) Option {
	switch e {
	case EnforcementHard, EnforcementAuditOnly:
	default:
		panic("httpx/budget: WithEnforcement requires a known enforcement mode")
	}
	return func(c *config) { c.enforcement = e }
}

// WithCleanupTimeout bounds the post-response refund and reconcile
// path. The cleanup context is detached from the request context
// (so cancellation cannot strand accounting) and capped at d.
// The duration must be positive. Default: 2s.
func WithCleanupTimeout(d time.Duration) Option {
	if d <= 0 {
		panic("httpx/budget: WithCleanupTimeout requires a positive duration")
	}
	return func(c *config) {
		c.cleanupTimeout = d
	}
}

// WithRefundOnTransportError refunds the optimistic pre-charge when the
// wrapped RoundTripper returns an error before a response is available.
//
// Leave this disabled for spend-control use cases unless the upstream's
// contract guarantees that failed requests are never charged.
func WithRefundOnTransportError() Option {
	return func(c *config) { c.refundOnError = true }
}

// Wrap returns a RoundTripper that pre-charges `b` for `key` on every
// request and reconciles the actual cost (when configured) on the
// response. base may be nil; defaults to a kit transport with the
// outbound TLS floor and connection-pool defaults applied.
//
// Panics on nil budget or empty key — neither is a safe runtime
// recovery condition.
func Wrap(base http.RoundTripper, b budget.Budget, key string, opts ...Option) http.RoundTripper {
	if b == nil {
		panic("httpx/budget: budget must not be nil")
	}
	if err := budget.ValidateKey(key); err != nil {
		panic("httpx/budget: key is invalid")
	}
	if base == nil {
		base = transportdefaults.New(nil, 0, "httpx/budget: Wrap")
	}
	cfg := config{
		defaultAmount:  1,
		logger:         slog.Default(),
		enforcement:    EnforcementHard,
		cleanupTimeout: defaultCleanupTimeout,
	}
	for _, o := range opts {
		if o == nil {
			panic("budget: Wrap option must not be nil")
		}
		o(&cfg)
	}
	if cfg.defaultAmount < 0 {
		panic("httpx/budget: default amount must be >= 0")
	}
	return &transport{base: base, b: b, key: key, cfg: cfg}
}

type transport struct {
	base http.RoundTripper
	b    budget.Budget
	key  string
	cfg  config
}

func (t *transport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, ErrInvalidRequest
	}
	estimate := t.estimate(req)

	allowed, _, _, err := t.b.Consume(req.Context(), t.key, estimate)
	if err != nil {
		return nil, fmt.Errorf("httpx/budget: pre-charge: %w", err)
	}
	if !allowed {
		return nil, ErrBudgetExceeded
	}

	resp, err := t.base.RoundTrip(req)
	if err != nil {
		if t.cfg.refundOnError {
			t.cleanupRefund(req.Context(), estimate, err)
		}
		return nil, err
	}

	if t.cfg.actualHeader != "" {
		if rerr := t.reconcile(req.Context(), resp, estimate); rerr != nil {
			_ = resp.Body.Close()
			return nil, rerr
		}
	}
	return resp, nil
}

// estimate reads the per-request estimate header if set, falling
// back to the configured default. Negative or unparseable values
// fall back to the default; we don't propagate user-supplied junk
// into the backend.
func (t *transport) estimate(req *http.Request) int64 {
	if t.cfg.estimateHeader == "" {
		return t.cfg.defaultAmount
	}
	v, _, ok := singletonHeaderValue(req.Header, t.cfg.estimateHeader)
	if !ok {
		return t.cfg.defaultAmount
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < 0 {
		return t.cfg.defaultAmount
	}
	return n
}

// reconcile computes the delta between estimate and actual reported
// by the upstream and either charges the under-estimate or refunds
// the over-estimate.
//
// Returns a non-nil error only under [EnforcementHard] when the
// delta cannot be charged; the caller closes the response body and
// surfaces the error. Header parse failures, refund-side errors,
// and audit-only mode never produce an error.
func (t *transport) reconcile(reqCtx context.Context, resp *http.Response, estimate int64) error {
	v, present, ok := singletonHeaderValue(resp.Header, t.cfg.actualHeader)
	if !present {
		return nil
	}
	if !ok {
		t.cfg.logger.Warn("httpx/budget: ambiguous actual header",
			"header", t.cfg.actualHeader)
		return nil
	}
	actual, err := strconv.ParseInt(v, 10, 64)
	if err != nil || actual < 0 {
		t.cfg.logger.Warn("httpx/budget: malformed actual header",
			"header", t.cfg.actualHeader, redact.String("value", v))
		return nil
	}
	delta := actual - estimate
	if delta == 0 {
		return nil
	}
	if delta < 0 {
		t.cleanupRefund(reqCtx, -delta, nil)
		return nil
	}

	ctx, cancel := t.cleanupContext(reqCtx)
	defer cancel()
	ok, _, _, cerr := t.b.Consume(ctx, t.key, delta)
	if cerr != nil {
		t.cfg.logger.Warn("httpx/budget: reconcile charge failed",
			redact.String("key", t.key), "delta", delta, redact.ErrorKey("err", cerr))
		if t.cfg.enforcement == EnforcementHard {
			t.cleanupRefund(reqCtx, estimate, cerr)
			return fmt.Errorf("httpx/budget: reconcile: %w", cerr)
		}
		return nil
	}
	if !ok {
		t.cfg.logger.Warn("httpx/budget: reconcile delta exceeded budget",
			redact.String("key", t.key), "delta", delta)
		if t.cfg.enforcement == EnforcementHard {
			t.cleanupRefund(reqCtx, estimate, nil)
			return ErrBudgetExceeded
		}
	}
	return nil
}

func singletonHeaderValue(h http.Header, name string) (value string, present bool, ok bool) {
	values := h.Values(name)
	if len(values) == 0 {
		return "", false, true
	}
	if len(values) != 1 {
		return "", true, false
	}
	value = strings.TrimSpace(values[0])
	if value == "" {
		return "", true, false
	}
	return value, true, true
}

// cleanupRefund credits `amount` back to the budget on a cleanup
// path. The context is detached from the request so a canceled
// caller cannot strand the refund, and is bounded by the configured
// cleanup timeout.
func (t *transport) cleanupRefund(reqCtx context.Context, amount int64, cause error) {
	if amount <= 0 {
		return
	}
	ctx, cancel := t.cleanupContext(reqCtx)
	defer cancel()
	_, ok, err := budget.Refund(ctx, t.b, t.key, amount)
	if err != nil {
		t.cfg.logger.Warn("httpx/budget: refund failed",
			redact.String("key", t.key), "amount", amount, redact.ErrorKey("err", err), redact.ErrorKey("cause", cause))
		return
	}
	if !ok {
		t.cfg.logger.Warn("httpx/budget: refund unavailable on backend",
			redact.String("key", t.key), "amount", amount, redact.ErrorKey("cause", cause))
	}
}

// cleanupContext returns a context detached from cancellation of
// reqCtx but capped at the configured cleanup timeout, so accounting
// always runs even when the client canceled.
func (t *transport) cleanupContext(reqCtx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(reqCtx), t.cfg.cleanupTimeout)
}
