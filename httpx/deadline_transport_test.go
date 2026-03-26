package httpx

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"
)

// mockRoundTripper captures the request context for inspection.
type mockRoundTripper struct {
	capturedCtx context.Context
	resp        *http.Response
	err         error
}

func (m *mockRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	m.capturedCtx = req.Context()
	return m.resp, m.err
}

func TestDeadlineBudgetTransport_WithDeadline(t *testing.T) {
	mock := &mockRoundTripper{
		resp: &http.Response{StatusCode: http.StatusOK},
	}
	transport := &deadlineBudgetTransport{
		base:         mock,
		safetyMargin: 500 * time.Millisecond,
		minTimeout:   1 * time.Second,
	}

	// Set a deadline 5 seconds from now.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://example.com", nil)
	resp, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	// The outbound context should have a tighter deadline than the original.
	outDeadline, ok := mock.capturedCtx.Deadline()
	if !ok {
		t.Fatal("expected outbound context to have a deadline")
	}
	origDeadline, _ := ctx.Deadline()
	if !outDeadline.Before(origDeadline) {
		t.Fatalf("outbound deadline %v should be before original deadline %v", outDeadline, origDeadline)
	}
}

func TestDeadlineBudgetTransport_NoDeadline(t *testing.T) {
	mock := &mockRoundTripper{
		resp: &http.Response{StatusCode: http.StatusOK},
	}
	transport := &deadlineBudgetTransport{
		base:         mock,
		safetyMargin: 500 * time.Millisecond,
		minTimeout:   1 * time.Second,
	}

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://example.com", nil)
	resp, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	// No deadline on original context means no deadline on outbound context.
	if _, ok := mock.capturedCtx.Deadline(); ok {
		t.Fatal("expected no deadline on outbound context when original has none")
	}
}

func TestDeadlineBudgetTransport_TightDeadlineUsesMinTimeout(t *testing.T) {
	mock := &mockRoundTripper{
		resp: &http.Response{StatusCode: http.StatusOK},
	}
	minTimeout := 2 * time.Second
	// remaining (~3s) minus safetyMargin (2.5s) = 0.5s < minTimeout (2s),
	// so the transport should clamp to minTimeout.
	transport := &deadlineBudgetTransport{
		base:         mock,
		safetyMargin: 2500 * time.Millisecond,
		minTimeout:   minTimeout,
	}

	parentCtx, parentCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer parentCancel()

	req, _ := http.NewRequestWithContext(parentCtx, http.MethodGet, "http://example.com", nil)
	now := time.Now()
	_, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	outDeadline, ok := mock.capturedCtx.Deadline()
	if !ok {
		t.Fatal("expected outbound context to have a deadline")
	}

	// The outbound deadline should be approximately now + minTimeout.
	expectedMin := now.Add(minTimeout - 500*time.Millisecond)
	if outDeadline.Before(expectedMin) {
		t.Fatalf("outbound deadline %v should be at least %v (minTimeout floor)", outDeadline, expectedMin)
	}
}

func TestDeadlineBudgetTransport_SafetyMarginLargerThanRemaining(t *testing.T) {
	mock := &mockRoundTripper{
		resp: &http.Response{StatusCode: http.StatusOK},
	}
	minTimeout := 1 * time.Second
	// safetyMargin (11s) > remaining (~10s), so result is negative → clamp to minTimeout.
	transport := &deadlineBudgetTransport{
		base:         mock,
		safetyMargin: 11 * time.Second,
		minTimeout:   minTimeout,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://example.com", nil)
	now := time.Now()
	_, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	outDeadline, ok := mock.capturedCtx.Deadline()
	if !ok {
		t.Fatal("expected outbound context to have a deadline")
	}

	// The transport should clamp to minTimeout (1s).
	expectedMin := now.Add(minTimeout - 500*time.Millisecond)
	expectedMax := now.Add(minTimeout + 500*time.Millisecond)
	if outDeadline.Before(expectedMin) || outDeadline.After(expectedMax) {
		t.Fatalf("outbound deadline %v should be approximately %v (minTimeout floor)", outDeadline, now.Add(minTimeout))
	}
}

func TestDeadlineBudgetTransport_BaseReceivesModifiedContext(t *testing.T) {
	mock := &mockRoundTripper{
		resp: &http.Response{StatusCode: http.StatusOK},
	}
	transport := &deadlineBudgetTransport{
		base:         mock,
		safetyMargin: 1 * time.Second,
		minTimeout:   500 * time.Millisecond,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Add a value to the context to verify it propagates.
	type ctxKey struct{}
	ctx = context.WithValue(ctx, ctxKey{}, "test-value")

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://example.com", nil)
	_, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify the value propagated through the new context.
	val, ok := mock.capturedCtx.Value(ctxKey{}).(string)
	if !ok || val != "test-value" {
		t.Fatalf("expected context value 'test-value', got %v", val)
	}

	// Verify the deadline was adjusted.
	outDeadline, ok := mock.capturedCtx.Deadline()
	if !ok {
		t.Fatal("expected outbound context to have a deadline")
	}
	origDeadline, _ := ctx.Deadline()
	if !outDeadline.Before(origDeadline) {
		t.Fatalf("outbound deadline %v should be before original %v", outDeadline, origDeadline)
	}
}

func TestNewResilientHTTPClient_WithDeadlineBudget(t *testing.T) {
	client := NewResilientHTTPClient(
		WithDeadlineBudget(
			WithSafetyMargin(200*time.Millisecond),
			WithMinTimeout(500*time.Millisecond),
		),
	)

	// The outermost transport should be a deadlineBudgetTransport.
	dbt, ok := client.Transport.(*deadlineBudgetTransport)
	if !ok {
		t.Fatalf("expected *deadlineBudgetTransport, got %T", client.Transport)
	}
	if dbt.safetyMargin != 200*time.Millisecond {
		t.Fatalf("expected safetyMargin 200ms, got %v", dbt.safetyMargin)
	}
	if dbt.minTimeout != 500*time.Millisecond {
		t.Fatalf("expected minTimeout 500ms, got %v", dbt.minTimeout)
	}

	// The inner transport should be a circuitBreakerTransport.
	if _, ok := dbt.base.(*circuitBreakerTransport); !ok {
		t.Fatalf("expected inner transport to be *circuitBreakerTransport, got %T", dbt.base)
	}
}

func TestNewResilientHTTPClient_WithoutDeadlineBudget(t *testing.T) {
	client := NewResilientHTTPClient()

	// Without deadline budget, the transport should be a circuitBreakerTransport directly.
	if _, ok := client.Transport.(*circuitBreakerTransport); !ok {
		t.Fatalf("expected *circuitBreakerTransport, got %T", client.Transport)
	}
}

func TestWithDeadlineBudget_DefaultOptions(t *testing.T) {
	client := NewResilientHTTPClient(WithDeadlineBudget())

	dbt, ok := client.Transport.(*deadlineBudgetTransport)
	if !ok {
		t.Fatalf("expected *deadlineBudgetTransport, got %T", client.Transport)
	}
	if dbt.safetyMargin != defaultSafetyMargin {
		t.Fatalf("expected default safetyMargin %v, got %v", defaultSafetyMargin, dbt.safetyMargin)
	}
	if dbt.minTimeout != defaultMinTimeout {
		t.Fatalf("expected default minTimeout %v, got %v", defaultMinTimeout, dbt.minTimeout)
	}
}

func TestDeadlineBudgetTransport_BaseTransportErrorPropagation(t *testing.T) {
	baseErr := errors.New("connection refused")

	t.Run("with deadline", func(t *testing.T) {
		mock := &mockRoundTripper{err: baseErr}
		transport := &deadlineBudgetTransport{
			base:         mock,
			safetyMargin: 500 * time.Millisecond,
			minTimeout:   1 * time.Second,
		}

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://example.com", nil)
		resp, err := transport.RoundTrip(req)
		if resp != nil {
			t.Fatal("expected nil response when base transport errors")
		}
		if !errors.Is(err, baseErr) {
			t.Fatalf("expected base error %v, got %v", baseErr, err)
		}
	})

	t.Run("without deadline", func(t *testing.T) {
		mock := &mockRoundTripper{err: baseErr}
		transport := &deadlineBudgetTransport{
			base:         mock,
			safetyMargin: 500 * time.Millisecond,
			minTimeout:   1 * time.Second,
		}

		req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://example.com", nil)
		resp, err := transport.RoundTrip(req)
		if resp != nil {
			t.Fatal("expected nil response when base transport errors")
		}
		if !errors.Is(err, baseErr) {
			t.Fatalf("expected base error %v, got %v", baseErr, err)
		}
	})
}

func TestWithSafetyMargin_IgnoresNegative(t *testing.T) {
	client := NewResilientHTTPClient(WithDeadlineBudget(
		WithSafetyMargin(-1 * time.Second),
	))
	dbt := client.Transport.(*deadlineBudgetTransport)
	if dbt.safetyMargin != defaultSafetyMargin {
		t.Fatalf("expected default safetyMargin %v after negative input, got %v", defaultSafetyMargin, dbt.safetyMargin)
	}
}

func TestDeadlineBudgetTransport_AlreadyExpiredParentContext(t *testing.T) {
	mock := &mockRoundTripper{
		resp: &http.Response{StatusCode: http.StatusOK},
		err:  context.DeadlineExceeded,
	}
	transport := &deadlineBudgetTransport{
		base:         mock,
		safetyMargin: 500 * time.Millisecond,
		minTimeout:   1 * time.Second,
	}

	// Create an already-expired context.
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Nanosecond)
	time.Sleep(5 * time.Millisecond) // ensure it has expired
	defer cancel()

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://example.com", nil)
	_, err := transport.RoundTrip(req)

	// The base transport should receive the request and return its error.
	// The derived context will use minTimeout since remaining budget is negative.
	if err == nil {
		t.Fatal("expected error from expired parent context")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected context.DeadlineExceeded, got %v", err)
	}

	// Verify the outbound context still got a deadline (clamped to minTimeout).
	outDeadline, ok := mock.capturedCtx.Deadline()
	if !ok {
		t.Fatal("outbound context should have a deadline even with expired parent")
	}
	if outDeadline.IsZero() {
		t.Fatal("outbound deadline should not be zero")
	}
}

func TestWithMinTimeout_IgnoresZeroAndNegative(t *testing.T) {
	client := NewResilientHTTPClient(WithDeadlineBudget(
		WithMinTimeout(0),
	))
	dbt := client.Transport.(*deadlineBudgetTransport)
	if dbt.minTimeout != defaultMinTimeout {
		t.Fatalf("expected default minTimeout %v after zero input, got %v", defaultMinTimeout, dbt.minTimeout)
	}

	client = NewResilientHTTPClient(WithDeadlineBudget(
		WithMinTimeout(-1 * time.Second),
	))
	dbt = client.Transport.(*deadlineBudgetTransport)
	if dbt.minTimeout != defaultMinTimeout {
		t.Fatalf("expected default minTimeout %v after negative input, got %v", defaultMinTimeout, dbt.minTimeout)
	}
}
