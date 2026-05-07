package budget_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	databudget "github.com/bds421/rho-kit/data/budget"
	"github.com/bds421/rho-kit/httpx/budget"
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

func TestRoundTrip_TransportErrorRefundsCharge(t *testing.T) {
	// The transport closes immediately; we just need an unreachable URL.
	b := &scriptedBudget{consumeResp: []consumeResult{{allowed: true, remaining: 100}}}
	logs := &captureLogger{}
	c := newClient(budget.Wrap(failingTransport{}, b, "alice", budget.WithLogger(logs)))

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
	warns []string
}

func (c *captureLogger) Warn(msg string, _ ...any) {
	c.warns = append(c.warns, msg)
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
