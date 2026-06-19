package httpx

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/bds421/rho-kit/resilience/v2/circuitbreaker"
)

// stubRoundTripper returns a fixed response/error and records how many times
// it was invoked and the request bodies it observed.
type stubRoundTripper struct {
	resp *http.Response
	err  error

	mu     sync.Mutex
	calls  int
	bodies []*trackingBody
}

func (s *stubRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	s.mu.Lock()
	s.calls++
	s.mu.Unlock()
	if req.Body != nil {
		if tb, ok := req.Body.(*trackingBody); ok {
			s.mu.Lock()
			s.bodies = append(s.bodies, tb)
			s.mu.Unlock()
		}
	}
	return s.resp, s.err
}

func (s *stubRoundTripper) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// trackingBody records whether Close was called.
type trackingBody struct {
	mu     sync.Mutex
	closed bool
}

func (b *trackingBody) Read(p []byte) (int, error) { return 0, io.EOF }

func (b *trackingBody) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.closed = true
	return nil
}

func (b *trackingBody) isClosed() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.closed
}

// newCBTransport builds a circuitBreakerTransport through the production
// constructor so the test exercises the exact breaker wiring used by
// NewResilientHTTPClient.
func newCBTransport(base http.RoundTripper, shouldTrip func(*http.Response, error) bool, threshold int, reset time.Duration) *circuitBreakerTransport {
	return newCircuitBreakerTransport(base, shouldTrip, threshold, reset, nil)
}

// Finding 1: WithCBShouldTrip must be able to exclude transport errors from
// tripping the breaker. A predicate that returns false for the transport error
// must NOT count toward the failure threshold, and the caller must still see
// the underlying error unwrapped.
func TestCircuitBreaker_ShouldTripFalse_DoesNotTripOnTransportError(t *testing.T) {
	transportErr := errors.New("boom")
	stub := &stubRoundTripper{err: transportErr}

	// Predicate excludes the transport error from tripping the breaker.
	neverTrip := func(_ *http.Response, _ error) bool { return false }

	rt := newCBTransport(stub, neverTrip, 2, time.Minute)

	for i := 0; i < 10; i++ {
		req, _ := http.NewRequest(http.MethodGet, "http://example.test", nil)
		resp, err := rt.RoundTrip(req)
		if resp != nil {
			_ = resp.Body.Close()
		}
		// The caller must still receive the underlying transport error.
		if !errors.Is(err, transportErr) {
			t.Fatalf("request %d: err = %v, want transport error %v", i, err, transportErr)
		}
		// It must never be reported as ErrCircuitOpen.
		if errors.Is(err, circuitbreaker.ErrCircuitOpen) {
			t.Fatalf("request %d: breaker tripped despite shouldTrip=false", i)
		}
	}

	if got := stub.callCount(); got != 10 {
		t.Fatalf("base transport called %d times, want 10 (breaker should never short-circuit)", got)
	}
}

// Finding 3 (default predicate): caller cancellation (context.Canceled) must not
// trip the breaker for a healthy downstream.
func TestCircuitBreaker_DefaultPredicate_IgnoresContextCanceled(t *testing.T) {
	stub := &stubRoundTripper{err: context.Canceled}

	rt := newCBTransport(stub, defaultShouldTrip, 3, time.Minute)

	for i := 0; i < 10; i++ {
		req, _ := http.NewRequest(http.MethodGet, "http://example.test", nil)
		resp, err := rt.RoundTrip(req)
		if resp != nil {
			_ = resp.Body.Close()
		}
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("request %d: err = %v, want context.Canceled", i, err)
		}
		if errors.Is(err, circuitbreaker.ErrCircuitOpen) {
			t.Fatalf("request %d: breaker tripped on caller cancellation", i)
		}
	}

	if got := stub.callCount(); got != 10 {
		t.Fatalf("base transport called %d times, want 10 (cancellations must not trip)", got)
	}
}

// Finding 3 (default predicate, counterpart): server-side deadline exceeded
// must still trip the breaker, since it signals downstream slowness.
func TestCircuitBreaker_DefaultPredicate_TripsOnDeadlineExceeded(t *testing.T) {
	stub := &stubRoundTripper{err: context.DeadlineExceeded}

	rt := newCBTransport(stub, defaultShouldTrip, 2, time.Minute)

	for i := 0; i < 2; i++ {
		req, _ := http.NewRequest(http.MethodGet, "http://example.test", nil)
		resp, err := rt.RoundTrip(req)
		if resp != nil {
			_ = resp.Body.Close()
		}
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("request %d: err = %v, want DeadlineExceeded", i, err)
		}
	}

	req, _ := http.NewRequest(http.MethodGet, "http://example.test", nil)
	resp, err := rt.RoundTrip(req)
	if resp != nil {
		_ = resp.Body.Close()
	}
	if !errors.Is(err, circuitbreaker.ErrCircuitOpen) {
		t.Fatalf("expected ErrCircuitOpen after deadline-exceeded threshold, got %v", err)
	}
}

// Finding 1 (counterpart): a predicate that DOES return true on the transport
// error must still trip the breaker as before.
func TestCircuitBreaker_ShouldTripTrue_TripsOnTransportError(t *testing.T) {
	transportErr := errors.New("boom")
	stub := &stubRoundTripper{err: transportErr}

	alwaysTrip := func(_ *http.Response, _ error) bool { return true }

	rt := newCBTransport(stub, alwaysTrip, 2, time.Minute)

	for i := 0; i < 2; i++ {
		req, _ := http.NewRequest(http.MethodGet, "http://example.test", nil)
		resp, err := rt.RoundTrip(req)
		if resp != nil {
			_ = resp.Body.Close()
		}
		if !errors.Is(err, transportErr) {
			t.Fatalf("request %d: err = %v, want transport error", i, err)
		}
	}

	req, _ := http.NewRequest(http.MethodGet, "http://example.test", nil)
	resp, err := rt.RoundTrip(req)
	if resp != nil {
		_ = resp.Body.Close()
	}
	if !errors.Is(err, circuitbreaker.ErrCircuitOpen) {
		t.Fatalf("expected ErrCircuitOpen after threshold reached, got %v", err)
	}
}

// A 5xx response counts as a failure for the breaker (via the serverError
// sentinel) but must still be returned to the caller as (resp, nil) with a
// readable, un-consumed body — the net/http convention where non-2xx is not an
// error. This exercises the serverError→(resp, nil) conversion path.
func TestCircuitBreaker_ServerError_ReturnsReadableResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, "boom-body")
	}))
	defer srv.Close()

	// threshold high enough that one 5xx does not open the breaker.
	rt := newCBTransport(http.DefaultTransport, defaultShouldTrip, 5, time.Minute)

	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("5xx must surface as (resp, nil), got err = %v", err)
	}
	if resp == nil {
		t.Fatal("expected a non-nil response for a 5xx")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", resp.StatusCode)
	}
	body, rerr := io.ReadAll(resp.Body)
	if rerr != nil {
		t.Fatalf("response body must be readable, got %v", rerr)
	}
	if string(body) != "boom-body" {
		t.Fatalf("body = %q, want %q", string(body), "boom-body")
	}
}

// A sustained run of 5xx responses must open the breaker; subsequent calls then
// short-circuit with ErrCircuitOpen instead of hitting the backend. This pins
// the threshold/serverError interaction end-to-end over httptest.
func TestCircuitBreaker_RepeatedServerErrors_OpenBreaker(t *testing.T) {
	var hits int
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		hits++
		mu.Unlock()
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	rt := newCBTransport(http.DefaultTransport, defaultShouldTrip, 2, time.Minute)

	for i := 0; i < 2; i++ {
		req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
		resp, err := rt.RoundTrip(req)
		if err != nil {
			t.Fatalf("request %d: 5xx must surface as (resp, nil), got %v", i, err)
		}
		_ = resp.Body.Close()
	}

	mu.Lock()
	hitsAfterThreshold := hits
	mu.Unlock()

	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	resp, err := rt.RoundTrip(req)
	if resp != nil {
		_ = resp.Body.Close()
	}
	if !errors.Is(err, circuitbreaker.ErrCircuitOpen) {
		t.Fatalf("expected ErrCircuitOpen after 5xx threshold, got %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if hits != hitsAfterThreshold {
		t.Fatalf("backend hit %d times after breaker opened; open circuit must short-circuit", hits-hitsAfterThreshold)
	}
}

// Finding 2: when the circuit is open and the closure never runs, RoundTrip must
// still close req.Body per the http.RoundTripper contract.
func TestCircuitBreaker_OpenCircuit_ClosesRequestBody(t *testing.T) {
	stub := &stubRoundTripper{err: errors.New("boom")}
	alwaysTrip := func(_ *http.Response, _ error) bool { return true }

	rt := newCBTransport(stub, alwaysTrip, 1, time.Minute)

	// Trip the breaker with a first request (threshold=1).
	req0, _ := http.NewRequest(http.MethodGet, "http://example.test", nil)
	_, err := rt.RoundTrip(req0)
	if err == nil {
		t.Fatal("expected error on first request")
	}

	// Now the circuit is open. A request with a body must have its body closed
	// even though base.RoundTrip is never invoked.
	body := &trackingBody{}
	req1, _ := http.NewRequest(http.MethodGet, "http://example.test", body)
	resp, err := rt.RoundTrip(req1)
	if resp != nil {
		_ = resp.Body.Close()
	}
	if !errors.Is(err, circuitbreaker.ErrCircuitOpen) {
		t.Fatalf("expected ErrCircuitOpen, got %v", err)
	}
	if !body.isClosed() {
		t.Fatal("req.Body was not closed when circuit was open (RoundTripper contract violation)")
	}
}
