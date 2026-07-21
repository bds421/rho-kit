package budget_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	databudget "github.com/bds421/rho-kit/data/v2/budget"
	"github.com/bds421/rho-kit/httpx/v2/budget"
)

// scriptedBudget is a hand-rolled stub. Each Consume/Refund call
// records its arguments and returns the configured response slice
// in order; if the script is exhausted the last entry repeats.
type scriptedBudget struct {
	consumeResp []consumeResult
	consumeIdx  atomic.Int32
	consumed    []consumeArg
	refunded    []consumeArg

	refundErr error
}

type consumeResult struct {
	allowed   bool
	remaining int64
	retry     time.Duration
	err       error
}

type consumeArg struct {
	key    string
	amount int64
}

func (s *scriptedBudget) Consume(_ context.Context, key string, amount int64) (bool, int64, time.Duration, error) {
	s.consumed = append(s.consumed, consumeArg{key, amount})
	idx := s.consumeIdx.Add(1) - 1
	if int(idx) >= len(s.consumeResp) {
		idx = int32(len(s.consumeResp) - 1)
	}
	r := s.consumeResp[idx]
	return r.allowed, r.remaining, r.retry, r.err
}

func (s *scriptedBudget) Peek(_ context.Context, _ string) (int64, error) { return 0, nil }

// Refund is implemented so the wrapper exercises the Refunder branch.
func (s *scriptedBudget) Refund(_ context.Context, key string, amount int64) (int64, error) {
	s.refunded = append(s.refunded, consumeArg{key, amount})
	return 0, s.refundErr
}

// nonRefunding lets us test the fallback path when the backend has no Refund.
type nonRefunding struct{ inner *scriptedBudget }

func (n *nonRefunding) Consume(ctx context.Context, key string, amount int64) (bool, int64, time.Duration, error) {
	return n.inner.Consume(ctx, key, amount)
}

func (n *nonRefunding) Peek(ctx context.Context, key string) (int64, error) {
	return n.inner.Peek(ctx, key)
}

func newClient(rt http.RoundTripper) *http.Client {
	return &http.Client{Transport: rt}
}

func upstream(t *testing.T, headers http.Header) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for k, vs := range headers {
			for _, v := range vs {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok")
		_ = r
	}))
}

func TestWrap_PanicsOnNilBudget(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on nil budget")
		}
	}()
	budget.Wrap(nil, nil, "k")
}

func TestWrap_PanicsOnEmptyKey(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on empty key")
		}
	}()
	budget.Wrap(nil, &scriptedBudget{}, "")
}

func TestWrap_PanicsOnInvalidKey(t *testing.T) {
	require.Panics(t, func() {
		budget.Wrap(nil, &scriptedBudget{}, "tenant acme")
	})
	require.Panics(t, func() {
		budget.Wrap(nil, &scriptedBudget{}, string([]byte{'k', 0xff}))
	})
}

func TestWrap_PanicsOnNilOption(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on nil option")
		}
	}()
	budget.Wrap(nil, &scriptedBudget{}, "k", nil)
}

func TestWrap_NilBaseUsesKitTransportWhenDefaultTransportReplaced(t *testing.T) {
	prev := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = prev })
	http.DefaultTransport = failingTransport{}

	srv := upstream(t, nil)
	t.Cleanup(srv.Close)

	b := &scriptedBudget{consumeResp: []consumeResult{{allowed: true, remaining: 99}}}
	c := newClient(budget.Wrap(nil, b, "alice"))

	resp, err := c.Get(srv.URL)
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestRoundTrip_NilRequestReturnsInvalidRequest(t *testing.T) {
	b := &scriptedBudget{consumeResp: []consumeResult{{allowed: true, remaining: 99}}}
	rt := budget.Wrap(http.DefaultTransport, b, "alice")

	resp, err := rt.RoundTrip(nil)

	assert.Nil(t, resp)
	assert.ErrorIs(t, err, budget.ErrInvalidRequest)
	assert.Empty(t, b.consumed, "invalid request must not be charged")
}

func TestWithCleanupTimeout_PanicsOnNonPositive(t *testing.T) {
	for _, d := range []time.Duration{0, -time.Second} {
		t.Run(d.String(), func(t *testing.T) {
			require.Panics(t, func() {
				budget.WithCleanupTimeout(d)
			})
		})
	}
}

func TestOptions_PanicOnInvalidInput(t *testing.T) {
	require.Panics(t, func() { budget.WithEstimateHeader("Bad Header") })
	require.Panics(t, func() { budget.WithActualHeader("Bad Header") })
	require.Panics(t, func() { budget.WithDefaultAmount(-1) })
	require.Panics(t, func() { budget.WithDefaultAmount(0) })
	require.Panics(t, func() { budget.WithEnforcement(budget.Enforcement(99)) })
}

func TestRoundTrip_PreChargesDefaultAmount(t *testing.T) {
	srv := upstream(t, nil)
	t.Cleanup(srv.Close)

	b := &scriptedBudget{consumeResp: []consumeResult{{allowed: true, remaining: 99}}}
	c := newClient(budget.Wrap(http.DefaultTransport, b, "alice"))

	resp, err := c.Get(srv.URL)
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	require.Len(t, b.consumed, 1)
	assert.Equal(t, consumeArg{"alice", 1}, b.consumed[0])
}

func TestRoundTrip_RejectsWithSentinel(t *testing.T) {
	srv := upstream(t, nil)
	t.Cleanup(srv.Close)
	b := &scriptedBudget{consumeResp: []consumeResult{{allowed: false, remaining: 0, retry: time.Hour}}}
	c := newClient(budget.Wrap(http.DefaultTransport, b, "alice"))

	resp, err := c.Get(srv.URL)
	if resp != nil {
		_ = resp.Body.Close()
	}
	require.Error(t, err)
	// The http.Client wraps RoundTrip errors in *url.Error; the
	// sentinel must remain reachable via errors.Is.
	assert.True(t, errors.Is(err, budget.ErrBudgetExceeded),
		"reject must surface ErrBudgetExceeded so callers can distinguish it from upstream 429")
}

func TestRoundTrip_RejectExposesRetryAfter(t *testing.T) {
	srv := upstream(t, nil)
	t.Cleanup(srv.Close)
	b := &scriptedBudget{consumeResp: []consumeResult{{allowed: false, remaining: 0, retry: 90 * time.Second}}}
	c := newClient(budget.Wrap(http.DefaultTransport, b, "alice"))

	resp, err := c.Get(srv.URL)
	if resp != nil {
		_ = resp.Body.Close()
	}
	require.Error(t, err)
	// Sentinel reachability is preserved.
	assert.True(t, errors.Is(err, budget.ErrBudgetExceeded))
	// The typed error must carry the store's backoff hint so callers can
	// schedule a retry without separately calling Peek.
	var be *budget.BudgetExceededError
	require.True(t, errors.As(err, &be), "reject must surface a *BudgetExceededError")
	assert.Equal(t, 90*time.Second, be.RetryAfter)
}

func TestRoundTrip_StripsEstimateHeaderBeforeUpstream(t *testing.T) {
	var gotEstimate string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotEstimate = r.Header.Get("X-Estimated-Tokens")
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	b := &scriptedBudget{consumeResp: []consumeResult{{allowed: true, remaining: 50}}}
	c := newClient(budget.Wrap(http.DefaultTransport, b, "alice",
		budget.WithEstimateHeader("X-Estimated-Tokens"),
	))

	req, _ := http.NewRequest(http.MethodPost, srv.URL, nil)
	req.Header.Set("X-Estimated-Tokens", "42")
	resp, err := c.Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })

	// The estimate was still charged...
	require.Len(t, b.consumed, 1)
	assert.Equal(t, int64(42), b.consumed[0].amount)
	// ...but the internal accounting header must not reach the upstream.
	assert.Empty(t, gotEstimate, "estimate header must be stripped before forwarding")

	// The caller's original request must not be mutated by the transport.
	assert.Equal(t, "42", req.Header.Get("X-Estimated-Tokens"),
		"transport must clone, not mutate, the caller's request")
}

func TestRoundTrip_AmbiguousEstimateHeaderWarns(t *testing.T) {
	srv := upstream(t, nil)
	t.Cleanup(srv.Close)
	b := &scriptedBudget{consumeResp: []consumeResult{{allowed: true, remaining: 50}}}
	logs := &captureLogger{}
	c := newClient(budget.Wrap(http.DefaultTransport, b, "alice",
		budget.WithEstimateHeader("X-Estimated-Tokens"),
		budget.WithDefaultAmount(7),
		budget.WithLogger(logs),
	))

	req, _ := http.NewRequest(http.MethodPost, srv.URL, nil)
	req.Header.Add("X-Estimated-Tokens", "42")
	req.Header.Add("X-Estimated-Tokens", "100")
	resp, err := c.Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })

	require.NotEmpty(t, logs.warns, "duplicate estimate header must be logged, like the actual-header path")
	assert.Contains(t, fmt.Sprint(logs.warns), "ambiguous estimate header")
}

func TestRoundTrip_MalformedEstimateHeaderWarnsRedacted(t *testing.T) {
	srv := upstream(t, nil)
	t.Cleanup(srv.Close)
	b := &scriptedBudget{consumeResp: []consumeResult{{allowed: true, remaining: 50}}}
	logs := &captureLogger{}
	c := newClient(budget.Wrap(http.DefaultTransport, b, "alice",
		budget.WithEstimateHeader("X-Estimated-Tokens"),
		budget.WithDefaultAmount(7),
		budget.WithLogger(logs),
	))

	req, _ := http.NewRequest(http.MethodPost, srv.URL, nil)
	req.Header.Set("X-Estimated-Tokens", "tenant-garbage")
	resp, err := c.Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })

	require.NotEmpty(t, logs.warns)
	assert.Contains(t, fmt.Sprint(logs.warns), "malformed estimate header")
	// The raw value must be redacted, mirroring the actual-header path.
	assert.NotContains(t, fmt.Sprint(logs.warnAttrs), "tenant-garbage")
}

func TestRoundTrip_BackendErrorPropagates(t *testing.T) {
	srv := upstream(t, nil)
	t.Cleanup(srv.Close)
	b := &scriptedBudget{consumeResp: []consumeResult{{err: errors.New("backend dead")}}}
	c := newClient(budget.Wrap(http.DefaultTransport, b, "alice"))

	resp, err := c.Get(srv.URL)
	if resp != nil {
		_ = resp.Body.Close()
	}
	require.Error(t, err)
	assert.False(t, errors.Is(err, budget.ErrBudgetExceeded),
		"backend error is NOT the rejection sentinel")
}

func TestRoundTrip_EstimateHeaderUsedWhenSet(t *testing.T) {
	srv := upstream(t, nil)
	t.Cleanup(srv.Close)
	b := &scriptedBudget{consumeResp: []consumeResult{{allowed: true, remaining: 50}}}
	c := newClient(budget.Wrap(http.DefaultTransport, b, "alice",
		budget.WithEstimateHeader("X-Estimated-Tokens"),
	))

	req, _ := http.NewRequest(http.MethodPost, srv.URL, nil)
	req.Header.Set("X-Estimated-Tokens", "42")
	resp, err := c.Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })

	require.Len(t, b.consumed, 1)
	assert.Equal(t, int64(42), b.consumed[0].amount)
}

func TestRoundTrip_EstimateHeaderDuplicateFallsBackToDefault(t *testing.T) {
	srv := upstream(t, nil)
	t.Cleanup(srv.Close)
	b := &scriptedBudget{consumeResp: []consumeResult{{allowed: true, remaining: 50}}}
	c := newClient(budget.Wrap(http.DefaultTransport, b, "alice",
		budget.WithEstimateHeader("X-Estimated-Tokens"),
		budget.WithDefaultAmount(7),
	))

	req, _ := http.NewRequest(http.MethodPost, srv.URL, nil)
	req.Header.Add("X-Estimated-Tokens", "42")
	req.Header.Add("X-Estimated-Tokens", "100")
	resp, err := c.Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })

	require.Len(t, b.consumed, 1)
	assert.Equal(t, int64(7), b.consumed[0].amount,
		"ambiguous estimate header must fall back to the configured default")
}

func TestRoundTrip_EstimateHeaderFallsBackOnGarbage(t *testing.T) {
	srv := upstream(t, nil)
	t.Cleanup(srv.Close)
	b := &scriptedBudget{consumeResp: []consumeResult{{allowed: true, remaining: 50}}}
	c := newClient(budget.Wrap(http.DefaultTransport, b, "alice",
		budget.WithEstimateHeader("X-Estimated-Tokens"),
		budget.WithDefaultAmount(7),
	))

	req, _ := http.NewRequest(http.MethodPost, srv.URL, nil)
	req.Header.Set("X-Estimated-Tokens", "not a number")
	resp, err := c.Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })

	require.Len(t, b.consumed, 1)
	assert.Equal(t, int64(7), b.consumed[0].amount,
		"unparseable estimate header must fall back to the default")
}

func TestRoundTrip_ReconcileChargesUnderEstimate(t *testing.T) {
	srv := upstream(t, http.Header{"X-Actual-Tokens": {"100"}})
	t.Cleanup(srv.Close)
	b := &scriptedBudget{
		consumeResp: []consumeResult{
			{allowed: true, remaining: 999}, // pre-charge
			{allowed: true, remaining: 899}, // reconcile delta
		},
	}
	c := newClient(budget.Wrap(http.DefaultTransport, b, "alice",
		budget.WithDefaultAmount(20),
		budget.WithActualHeader("X-Actual-Tokens"),
	))

	resp, err := c.Get(srv.URL)
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })

	require.Len(t, b.consumed, 2)
	assert.Equal(t, int64(20), b.consumed[0].amount, "first call is the estimate")
	assert.Equal(t, int64(80), b.consumed[1].amount, "second call charges the under-estimate delta")
}

func TestRoundTrip_DuplicateActualHeaderSkipsReconciliation(t *testing.T) {
	srv := upstream(t, http.Header{"X-Actual-Tokens": {"5", "100"}})
	t.Cleanup(srv.Close)
	b := &scriptedBudget{
		consumeResp: []consumeResult{
			{allowed: true, remaining: 999},
			{allowed: true, remaining: 899},
		},
	}
	logs := &captureLogger{}
	c := newClient(budget.Wrap(http.DefaultTransport, b, "alice",
		budget.WithDefaultAmount(20),
		budget.WithActualHeader("X-Actual-Tokens"),
		budget.WithLogger(logs),
	))

	resp, err := c.Get(srv.URL)
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })

	require.Len(t, b.consumed, 1, "ambiguous actual header must not charge a delta")
	assert.Empty(t, b.refunded, "ambiguous actual header must not refund from a chosen value")
	require.NotEmpty(t, logs.warns, "ambiguous actual header should warn operators")
}

func TestRoundTrip_MalformedActualHeaderLogRedactsValue(t *testing.T) {
	srv := upstream(t, http.Header{"X-Actual-Tokens": {"tenant-secret-tokens"}})
	t.Cleanup(srv.Close)
	b := &scriptedBudget{consumeResp: []consumeResult{{allowed: true, remaining: 999}}}
	logs := &captureLogger{}
	c := newClient(budget.Wrap(http.DefaultTransport, b, "tenant-secret-key",
		budget.WithDefaultAmount(20),
		budget.WithActualHeader("X-Actual-Tokens"),
		budget.WithLogger(logs),
	))

	resp, err := c.Get(srv.URL)
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })

	require.NotEmpty(t, logs.warns)
	attrs := fmt.Sprint(logs.warnAttrs)
	assert.NotContains(t, attrs, "tenant-secret-tokens")
	assert.NotContains(t, attrs, "tenant-secret-key")
	assert.Contains(t, attrs, "<redacted")
}

func TestRoundTrip_ReconcileRefundsOverEstimate(t *testing.T) {
	srv := upstream(t, http.Header{"X-Actual-Tokens": {"5"}})
	t.Cleanup(srv.Close)
	b := &scriptedBudget{
		consumeResp: []consumeResult{{allowed: true, remaining: 999}},
	}
	c := newClient(budget.Wrap(http.DefaultTransport, b, "alice",
		budget.WithDefaultAmount(50),
		budget.WithActualHeader("X-Actual-Tokens"),
	))

	resp, err := c.Get(srv.URL)
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })

	require.Len(t, b.refunded, 1)
	assert.Equal(t, consumeArg{"alice", 45}, b.refunded[0],
		"over-estimate must be refunded as estimate-actual")
}

func TestRoundTrip_ReconcileNoOpWhenActualEqualsEstimate(t *testing.T) {
	srv := upstream(t, http.Header{"X-Actual-Tokens": {"50"}})
	t.Cleanup(srv.Close)
	b := &scriptedBudget{consumeResp: []consumeResult{{allowed: true, remaining: 999}}}
	c := newClient(budget.Wrap(http.DefaultTransport, b, "alice",
		budget.WithDefaultAmount(50),
		budget.WithActualHeader("X-Actual-Tokens"),
	))

	resp, err := c.Get(srv.URL)
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })

	assert.Len(t, b.consumed, 1, "no second consume when delta is zero")
	assert.Empty(t, b.refunded, "no refund when delta is zero")
}

func TestRoundTrip_ReconcileSkippedWhenActualHeaderUnconfigured(t *testing.T) {
	srv := upstream(t, http.Header{"X-Actual-Tokens": {"99"}})
	t.Cleanup(srv.Close)
	b := &scriptedBudget{consumeResp: []consumeResult{{allowed: true, remaining: 999}}}
	c := newClient(budget.Wrap(http.DefaultTransport, b, "alice"))

	resp, err := c.Get(srv.URL)
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })

	assert.Len(t, b.consumed, 1, "without WithActualHeader the wrapper does not reconcile")
}

func TestRoundTrip_RefundFallbackForNonRefunder(t *testing.T) {
	srv := upstream(t, http.Header{"X-Actual-Tokens": {"5"}})
	t.Cleanup(srv.Close)
	inner := &scriptedBudget{consumeResp: []consumeResult{{allowed: true, remaining: 999}}}
	wrapped := &nonRefunding{inner: inner}

	logs := &captureLogger{}
	c := newClient(budget.Wrap(http.DefaultTransport, wrapped, "alice",
		budget.WithDefaultAmount(50),
		budget.WithActualHeader("X-Actual-Tokens"),
		budget.WithLogger(logs),
	))

	resp, err := c.Get(srv.URL)
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })

	assert.Empty(t, inner.refunded, "wrapped budget without Refund must not see a refund call")
	require.NotEmpty(t, logs.warns, "missed refund must log a warning so operators can see the gap")
}

func TestRoundTrip_TransportErrorRetainsChargeByDefault(t *testing.T) {
	b := &scriptedBudget{consumeResp: []consumeResult{{allowed: true, remaining: 100}}}
	c := newClient(budget.Wrap(failingTransport{}, b, "alice"))

	resp, err := c.Get("http://example.invalid/")
	if resp != nil {
		_ = resp.Body.Close()
	}
	require.Error(t, err)
	require.Len(t, b.consumed, 1)
	assert.Empty(t, b.refunded, "ambiguous transport failures must retain the optimistic charge by default")
}

func TestRoundTrip_TransportErrorRefundsChargeWhenEnabled(t *testing.T) {
	b := &scriptedBudget{consumeResp: []consumeResult{{allowed: true, remaining: 100}}}
	c := newClient(budget.Wrap(failingTransport{}, b, "alice", budget.WithRefundOnTransportError()))

	resp, err := c.Get("http://example.invalid/")
	if resp != nil {
		_ = resp.Body.Close()
	}
	require.Error(t, err)
	require.Len(t, b.refunded, 1, "transport failure must refund the optimistic pre-charge")
	assert.Equal(t, int64(1), b.refunded[0].amount)
}

// fakeRoundTripper that fails so we can exercise the transport-error path.
type failingTransport struct{}

func (failingTransport) RoundTrip(_ *http.Request) (*http.Response, error) {
	return nil, errors.New("transport boom")
}

type captureLogger struct {
	warns     []string
	warnAttrs [][]any
}

func (c *captureLogger) Warn(msg string, attrs ...any) {
	c.warns = append(c.warns, msg)
	c.warnAttrs = append(c.warnAttrs, attrs)
}

// TestSentinelString tightens the sentinel's identity so wrappers
// can include it in error chains without losing the ability to
// detect it via errors.Is — small but load-bearing for the spec's
// requirement to distinguish "we said no" from "upstream said no".
func TestSentinelString(t *testing.T) {
	assert.Equal(t, "httpx/budget: budget exceeded", budget.ErrBudgetExceeded.Error())
}

// Ensure the test-only failingTransport implementation also gets
// to exercise the wrapper's handling of underlying RoundTrippers
// that succeed. This keeps the `databudget` import live so the
// build hasn't dropped the dependency in some refactor.
func TestSentinelDistinctFromBudgetSentinels(t *testing.T) {
	assert.NotErrorIs(t, budget.ErrBudgetExceeded, databudget.ErrInvalidKey)
	assert.NotErrorIs(t, budget.ErrBudgetExceeded, databudget.ErrInvalidAmount)
	// And avoid an "unused import" when assertions evolve.
	_ = strconv.Itoa
}

// TestRoundTrip_HardEnforcementRejectsOnDeltaExceedsBudget pins the
// new spec contract: when reconcile reveals the upstream cost > what
// the budget can pay, the response is closed and ErrBudgetExceeded
// surfaces so the caller cannot read bytes the budget did not
// authorize. The pre-charge is retained (not refunded) so repeated
// over-actual loops drain budget instead of spending free.
func TestRoundTrip_HardEnforcementRejectsOnDeltaExceedsBudget(t *testing.T) {
	srv := upstream(t, http.Header{"X-Actual-Tokens": {"100"}})
	t.Cleanup(srv.Close)
	b := &scriptedBudget{
		consumeResp: []consumeResult{
			{allowed: true, remaining: 999}, // pre-charge succeeds
			{allowed: false, remaining: 0},  // delta charge denied
		},
	}
	c := newClient(budget.Wrap(http.DefaultTransport, b, "alice",
		budget.WithDefaultAmount(20),
		budget.WithActualHeader("X-Actual-Tokens"),
	))

	resp, err := c.Get(srv.URL)
	if resp != nil {
		_ = resp.Body.Close()
	}
	require.Error(t, err)
	assert.True(t, errors.Is(err, budget.ErrBudgetExceeded),
		"hard enforcement must surface ErrBudgetExceeded for delta-denied")
	require.Empty(t, b.refunded, "hard over-actual must retain the pre-charge (no free loop)")
}

// TestRoundTrip_HardEnforcementRejectsOnDeltaBackendError covers the
// other failure mode: backend error during the delta charge. Without
// hard enforcement the caller would receive the response despite the
// budget having lost track of the actual cost.
func TestRoundTrip_HardEnforcementRejectsOnDeltaBackendError(t *testing.T) {
	srv := upstream(t, http.Header{"X-Actual-Tokens": {"100"}})
	t.Cleanup(srv.Close)
	b := &scriptedBudget{
		consumeResp: []consumeResult{
			{allowed: true, remaining: 999},
			{err: errors.New("redis down")},
		},
	}
	c := newClient(budget.Wrap(http.DefaultTransport, b, "alice",
		budget.WithDefaultAmount(20),
		budget.WithActualHeader("X-Actual-Tokens"),
	))

	resp, err := c.Get(srv.URL)
	if resp != nil {
		_ = resp.Body.Close()
	}
	require.Error(t, err)
}

// TestRoundTrip_AuditOnlyKeepsLegacyBehavior pins that callers can
// opt out of enforcement and keep the historical "log and return"
// shape for accounting-only deployments.
func TestRoundTrip_AuditOnlyKeepsLegacyBehavior(t *testing.T) {
	srv := upstream(t, http.Header{"X-Actual-Tokens": {"100"}})
	t.Cleanup(srv.Close)
	b := &scriptedBudget{
		consumeResp: []consumeResult{
			{allowed: true, remaining: 999},
			{allowed: false, remaining: 0},
		},
	}
	logs := &captureLogger{}
	c := newClient(budget.Wrap(http.DefaultTransport, b, "alice",
		budget.WithDefaultAmount(20),
		budget.WithActualHeader("X-Actual-Tokens"),
		budget.WithEnforcement(budget.EnforcementAuditOnly),
		budget.WithLogger(logs),
	))

	resp, err := c.Get(srv.URL)
	require.NoError(t, err, "audit-only must not reject the response")
	t.Cleanup(func() { _ = resp.Body.Close() })
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	require.NotEmpty(t, logs.warns, "audit-only must still warn on denied delta")
	assert.Empty(t, b.refunded, "audit-only must not refund the estimate on a denied delta")
}

// TestRoundTrip_CleanupRunsOnCanceledContext: a canceled request
// context must not strand the refund on a transport error. Without
// the WithoutCancel + bounded timeout fix, the refund would inherit
// the canceled context and the backend call could fail or be skipped.
func TestRoundTrip_CleanupRunsOnCanceledContext(t *testing.T) {
	b := &scriptedBudget{consumeResp: []consumeResult{{allowed: true, remaining: 100}}}
	c := newClient(budget.Wrap(observingTransport{
		fn: func(req *http.Request) (*http.Response, error) {
			return nil, errors.New("transport boom")
		},
	}, b, "alice", budget.WithRefundOnTransportError()))

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // canceled BEFORE the request runs
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://example.invalid/", nil)
	resp, err := c.Do(req)
	if resp != nil {
		_ = resp.Body.Close()
	}
	require.Error(t, err)
	require.Len(t, b.refunded, 1, "cleanup refund must run even when the request context is canceled")
	assert.Equal(t, int64(1), b.refunded[0].amount)
}

// TestWithLoggerNilNormalizes verifies WithLogger(nil) does not
// disarm the default logger and therefore cannot panic on warning
// paths. Pattern A from the kit-wide R2 audit: loggers normalize.
func TestWithLoggerNilNormalizes(t *testing.T) {
	srv := upstream(t, http.Header{"X-Actual-Tokens": {"oops"}})
	t.Cleanup(srv.Close)
	b := &scriptedBudget{consumeResp: []consumeResult{{allowed: true, remaining: 100}}}
	c := newClient(budget.Wrap(http.DefaultTransport, b, "alice",
		budget.WithActualHeader("X-Actual-Tokens"),
		budget.WithLogger(nil),
	))

	// Malformed actual header walks the warning path; with nil
	// not normalized this would panic.
	resp, err := c.Get(srv.URL)
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })
}

// observingTransport lets a test inject a function for RoundTrip
// while keeping the resp.Body lifecycle assertions tight.
type observingTransport struct {
	fn func(*http.Request) (*http.Response, error)
}

func (o observingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return o.fn(req)
}

// trackingBody records whether Close was called so the
// RoundTripper-contract assertions ("RoundTrip must always close the
// body, including on errors") can be made.
type trackingBody struct {
	closed atomic.Bool
}

func (b *trackingBody) Read(_ []byte) (int, error) { return 0, io.EOF }
func (b *trackingBody) Close() error {
	b.closed.Store(true)
	return nil
}

// TestRoundTrip_ClosesBodyOnPreChargeReject pins the RoundTripper
// contract for the budget-exceeded early return: a request whose
// pre-charge is denied never reaches base.RoundTrip, so the wrapper
// itself must close req.Body or file/pipe-backed bodies leak.
func TestRoundTrip_ClosesBodyOnPreChargeReject(t *testing.T) {
	b := &scriptedBudget{consumeResp: []consumeResult{{allowed: false, remaining: 0, retry: time.Hour}}}
	rt := budget.Wrap(http.DefaultTransport, b, "alice")

	body := &trackingBody{}
	req, err := http.NewRequest(http.MethodPost, "http://example.invalid/", body)
	require.NoError(t, err)

	resp, rerr := rt.RoundTrip(req)
	if resp != nil {
		_ = resp.Body.Close()
	}
	require.ErrorIs(t, rerr, budget.ErrBudgetExceeded)
	assert.True(t, body.closed.Load(),
		"RoundTrip must close req.Body even when the pre-charge is rejected")
}

// TestRoundTrip_ClosesBodyOnPreChargeBackendError pins the same
// contract for the backend-error early return.
func TestRoundTrip_ClosesBodyOnPreChargeBackendError(t *testing.T) {
	b := &scriptedBudget{consumeResp: []consumeResult{{err: errors.New("backend dead")}}}
	rt := budget.Wrap(http.DefaultTransport, b, "alice")

	body := &trackingBody{}
	req, err := http.NewRequest(http.MethodPost, "http://example.invalid/", body)
	require.NoError(t, err)

	resp, rerr := rt.RoundTrip(req)
	if resp != nil {
		_ = resp.Body.Close()
	}
	require.Error(t, rerr)
	assert.False(t, errors.Is(rerr, budget.ErrBudgetExceeded))
	assert.True(t, body.closed.Load(),
		"RoundTrip must close req.Body even when the pre-charge backend errors")
}

// TestRoundTrip_EstimateHeaderZeroFallsBackToDefault pins that an
// untrusted estimate header of "0" does not bypass the budget gate.
// A zero charge is a no-op probe that the backend admits even when the
// budget is exhausted, so a caller-supplied X-Estimated-Tokens: 0
// must fall back to the configured default just like negative/garbage
// values do.
func TestRoundTrip_EstimateHeaderZeroFallsBackToDefault(t *testing.T) {
	srv := upstream(t, nil)
	t.Cleanup(srv.Close)
	b := &scriptedBudget{consumeResp: []consumeResult{{allowed: true, remaining: 50}}}
	c := newClient(budget.Wrap(http.DefaultTransport, b, "alice",
		budget.WithEstimateHeader("X-Estimated-Tokens"),
		budget.WithDefaultAmount(7),
	))

	req, _ := http.NewRequest(http.MethodPost, srv.URL, nil)
	req.Header.Set("X-Estimated-Tokens", "0")
	resp, err := c.Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })

	require.Len(t, b.consumed, 1)
	assert.Equal(t, int64(7), b.consumed[0].amount,
		"zero estimate header must fall back to the default so it cannot bypass the gate")
}
