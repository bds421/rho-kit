package httpx

import (
	"context"
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
	transport := &deadlineBudgetTransport{
		base:         mock,
		safetyMargin: 500 * time.Millisecond,
		minTimeout:   minTimeout,
	}

	// Use a parent context with a generous deadline so the minTimeout floor
	// is observable. The remaining budget (200ms) minus safety margin (500ms)
	// is negative, so the transport should use minTimeout (2s). Because the
	// parent deadline is 10s, the child context.WithTimeout(2s) wins.
	parentCtx, parentCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer parentCancel()

	// Create a derived context that simulates a tight remaining budget:
	// remaining = 200ms, but parent deadline is still 10s away.
	// We achieve this by directly setting the deadline on the transport's
	// perspective: the transport reads time.Until(deadline) - safetyMargin.
	// With a 10s parent, remaining is ~10s, minus 500ms = ~9.5s, which is
	// above minTimeout. Instead, we test with a short remaining.
	//
	// To properly test the floor: set remaining (3s) minus safetyMargin (2.5s) = 0.5s < minTimeout (2s).
	transport.safetyMargin = 2500 * time.Millisecond

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

	// The transport should have applied minTimeout (2s) as the floor.
	// The outbound deadline should be approximately now + 2s.
	expectedMin := now.Add(minTimeout - 200*time.Millisecond)
	if outDeadline.Before(expectedMin) {
		t.Fatalf("outbound deadline %v should be at least %v (minTimeout floor)", outDeadline, expectedMin)
	}
}

func TestDeadlineBudgetTransport_SafetyMarginLargerThanRemaining(t *testing.T) {
	mock := &mockRoundTripper{
		resp: &http.Response{StatusCode: http.StatusOK},
	}
	minTimeout := 1 * time.Second
	transport := &deadlineBudgetTransport{
		base:         mock,
		safetyMargin: 3 * time.Second,
		minTimeout:   minTimeout,
	}

	// Parent has 10s deadline, but remaining (10s) - safetyMargin (3s) = 7s > minTimeout.
	// To test the floor: use a 4s parent. remaining (4s) - safetyMargin (3s) = 1s = minTimeout.
	// Use a 2s parent: remaining (2s) - safetyMargin (3s) = -1s < minTimeout → clamp to 1s.
	// Parent deadline is 10s, so context.WithTimeout(1s) will produce a 1s deadline.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Override safetyMargin to be larger than remaining budget would suggest.
	// With 10s parent and 11s safety margin, remaining - margin is negative.
	transport.safetyMargin = 11 * time.Second

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
	expectedMin := now.Add(minTimeout - 200*time.Millisecond)
	expectedMax := now.Add(minTimeout + 200*time.Millisecond)
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
