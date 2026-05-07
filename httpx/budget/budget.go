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
// The reconciliation step is best-effort: a refund failure is logged
// (via the [Logger] option) but does not surface to the caller. A
// charge failure for the *delta* propagates as an error so the
// caller can decide what to do (back off, escalate, etc.).
//
// # Sentinel error
//
// When the budget rejects the pre-charge, [ErrBudgetExceeded] is
// returned. Callers can `errors.Is(err, budget.ErrBudgetExceeded)`
// to distinguish "we said no" from upstream HTTP 429s.
package budget

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/bds421/rho-kit/data/budget"
)

// ErrBudgetExceeded is the sentinel returned by RoundTrip when the
// pre-charge fails. Callers compare with [errors.Is].
var ErrBudgetExceeded = errors.New("httpx/budget: budget exceeded")

// Logger is the minimal interface accepted by [WithLogger]. *slog.Logger
// satisfies it.
type Logger interface {
	Warn(msg string, args ...any)
}

// Option configures the [Wrap]ed RoundTripper.
type Option func(*config)

type config struct {
	estimateHeader string
	actualHeader   string
	defaultAmount  int64
	logger         Logger
}

// WithEstimateHeader names a request header whose integer value is
// charged instead of the default. Use this when callers can compute
// a per-request cost upstream (e.g. tokenising the prompt before
// dispatch). When the header is absent or unparseable the default
// amount is charged instead.
func WithEstimateHeader(name string) Option {
	return func(c *config) { c.estimateHeader = name }
}

// WithActualHeader names a response header whose integer value is
// the authoritative cost. The wrapper reconciles the difference
// between the estimate and the actual after the upstream returns.
// Set "" to disable reconciliation.
func WithActualHeader(name string) Option {
	return func(c *config) { c.actualHeader = name }
}

// WithDefaultAmount sets the per-request charge when no estimate
// header is set. Default: 1.
func WithDefaultAmount(n int64) Option {
	return func(c *config) { c.defaultAmount = n }
}

// WithLogger sets the logger used for non-fatal reconciliation
// warnings. Default: slog.Default().
func WithLogger(l Logger) Option {
	return func(c *config) { c.logger = l }
}

// Wrap returns a RoundTripper that pre-charges `b` for `key` on every
// request and reconciles the actual cost (when configured) on the
// response. base may be nil; defaults to [http.DefaultTransport].
//
// Panics on nil budget or empty key — neither is a safe runtime
// recovery condition.
func Wrap(base http.RoundTripper, b budget.Budget, key string, opts ...Option) http.RoundTripper {
	if b == nil {
		panic("httpx/budget: budget must not be nil")
	}
	if key == "" {
		panic("httpx/budget: key must not be empty")
	}
	if base == nil {
		base = http.DefaultTransport
	}
	cfg := config{
		defaultAmount: 1,
		logger:        slog.Default(),
	}
	for _, o := range opts {
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
	estimate := t.estimate(req)

	// Pre-charge against the configured budget. A reject here means
	// we never dispatch the request — the upstream sees no traffic.
	allowed, _, _, err := t.b.Consume(req.Context(), t.key, estimate)
	if err != nil {
		return nil, fmt.Errorf("httpx/budget: pre-charge: %w", err)
	}
	if !allowed {
		return nil, ErrBudgetExceeded
	}

	resp, err := t.base.RoundTrip(req)
	if err != nil {
		// Refund the optimistic charge so transport errors don't
		// nibble at the budget. We don't hard-fail on refund errors
		// because the caller already has a transport error.
		t.refund(req.Context(), estimate, err)
		return nil, err
	}

	if t.cfg.actualHeader != "" {
		t.reconcile(req.Context(), resp, estimate)
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
	v := req.Header.Get(t.cfg.estimateHeader)
	if v == "" {
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
// A failure to reconcile is non-fatal: the response is still
// returned to the caller; we just log the gap so an operator can
// notice when reconciliation is consistently broken (e.g. a
// misconfigured header name).
func (t *transport) reconcile(ctx context.Context, resp *http.Response, estimate int64) {
	v := resp.Header.Get(t.cfg.actualHeader)
	if v == "" {
		return
	}
	actual, err := strconv.ParseInt(v, 10, 64)
	if err != nil || actual < 0 {
		t.cfg.logger.Warn("httpx/budget: malformed actual header",
			"header", t.cfg.actualHeader, "value", v)
		return
	}
	delta := actual - estimate
	if delta == 0 {
		return
	}
	if delta > 0 {
		// Under-estimated; charge the difference. We don't refuse
		// the response on rejection — the request already happened
		// and the user already got the data; pulling the rug here
		// would be confusing. Just log so operators see chronic
		// under-charging.
		ok, _, _, err := t.b.Consume(ctx, t.key, delta)
		if err != nil {
			t.cfg.logger.Warn("httpx/budget: reconcile charge failed",
				"key", t.key, "delta", delta, "err", err)
			return
		}
		if !ok {
			t.cfg.logger.Warn("httpx/budget: reconcile delta exceeded budget",
				"key", t.key, "delta", delta)
		}
		return
	}
	// Over-estimated; refund the gap. Backends without [budget.Refunder]
	// cannot credit back — the missed amount converges at the next
	// period boundary, which is bounded by `period`.
	t.refund(ctx, -delta, nil)
}

// refund credits `amount` back to the budget via the optional
// [budget.Refunder] capability. Backends without it lose the credit
// for the current period — bounded by the period length, which
// converges to zero at the boundary.
func (t *transport) refund(ctx context.Context, amount int64, cause error) {
	if amount <= 0 {
		return
	}
	_, ok, err := budget.Refund(ctx, t.b, t.key, amount)
	if err != nil {
		t.cfg.logger.Warn("httpx/budget: refund failed",
			"key", t.key, "amount", amount, "err", err, "cause", cause)
		return
	}
	if !ok {
		t.cfg.logger.Warn("httpx/budget: refund unavailable on backend",
			"key", t.key, "amount", amount, "cause", cause)
	}
}
