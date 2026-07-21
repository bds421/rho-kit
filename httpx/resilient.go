package httpx

import (
	"context"
	"crypto/tls"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/bds421/rho-kit/resilience/v2/circuitbreaker"
)

// ResilientOption configures a resilient HTTP client.
type ResilientOption func(*resilientConfig)

type resilientConfig struct {
	timeout           time.Duration
	tlsConfig         *tls.Config
	cbThreshold       int
	cbReset           time.Duration
	shouldTrip        func(resp *http.Response, err error) bool
	onStateChange     func(from, to circuitbreaker.State)
	hostCircuitBreakers bool
	deadlineBudget    bool
	deadlineBudgetCfg deadlineBudgetConfig
	checkRedirect     func(*http.Request, []*http.Request) error
	idleConnTimeout   time.Duration
}

// WithResilientTimeout sets the HTTP client timeout. Default: 10s.
func WithResilientTimeout(d time.Duration) ResilientOption {
	if d <= 0 {
		panic("httpx: WithResilientTimeout requires a positive duration")
	}
	return func(c *resilientConfig) { c.timeout = d }
}

// WithoutResilientTimeout removes the static http.Client timeout. Use only
// when every request is bounded by a caller context deadline, typically with
// [WithDeadlineBudget].
func WithoutResilientTimeout() ResilientOption {
	return func(c *resilientConfig) { c.timeout = 0 }
}

// WithResilientTLS sets the TLS configuration for the transport.
func WithResilientTLS(cfg *tls.Config) ResilientOption {
	if cfg == nil {
		panic("httpx: WithResilientTLS requires a non-nil tls.Config")
	}
	owned := cloneTLSConfigWithFloor(cfg, "httpx: WithResilientTLS")
	return func(c *resilientConfig) { c.tlsConfig = cloneTLSConfigWithFloor(owned, "httpx: WithResilientTLS") }
}

// WithResilientIdleConnTimeout sets the resilient client's idle connection
// timeout. It mirrors [WithIdleConnTimeout] for clients that also need circuit
// breaker protection.
func WithResilientIdleConnTimeout(d time.Duration) ResilientOption {
	if d < 0 {
		panic("httpx: WithResilientIdleConnTimeout requires a non-negative duration")
	}
	return func(c *resilientConfig) { c.idleConnTimeout = d }
}

// WithResilientFollowRedirects enables bounded redirect following for
// resilient HTTP clients. By default redirects are blocked with
// [ErrRedirectBlocked], matching [NewHTTPClient].
func WithResilientFollowRedirects(maxHops int) ResilientOption {
	if maxHops <= 0 {
		panic("httpx: WithResilientFollowRedirects requires maxHops > 0")
	}
	return func(c *resilientConfig) { c.checkRedirect = boundedRedirectPolicy(maxHops) }
}

// WithCBThreshold sets the consecutive failure count to trip the circuit
// breaker. Default: 5.
func WithCBThreshold(n int) ResilientOption {
	if n <= 0 {
		panic("httpx: WithCBThreshold requires a positive threshold")
	}
	return func(c *resilientConfig) {
		c.cbThreshold = n
	}
}

// WithCBResetTimeout sets how long the circuit stays open before allowing
// a probe request. Default: 30s.
func WithCBResetTimeout(d time.Duration) ResilientOption {
	if d <= 0 {
		panic("httpx: WithCBResetTimeout requires a positive duration")
	}
	return func(c *resilientConfig) { c.cbReset = d }
}

// WithCBShouldTrip sets a custom predicate for deciding whether a response/error
// should count toward the circuit breaker failure threshold. By default,
// transport errors and HTTP 5xx responses trip the breaker, except caller-driven
// cancellation (context.Canceled), which is never counted.
//
// Returning false for a non-nil error excludes that error from the failure
// count: the breaker neither advances toward tripping nor resets the
// consecutive-failure streak, and the caller still receives the original
// error from RoundTrip. This lets callers exclude e.g. context.Canceled or
// DNS errors from opening the breaker without healing a half-open circuit.
//
// Panics if fn is nil — a nil predicate would compile but crash on the
// first outbound request through the transport, long after construction.
func WithCBShouldTrip(fn func(resp *http.Response, err error) bool) ResilientOption {
	if fn == nil {
		panic("httpx: WithCBShouldTrip requires a non-nil predicate")
	}
	return func(c *resilientConfig) { c.shouldTrip = fn }
}

// WithCBOnStateChange registers a callback for circuit breaker state transitions.
//
// Panics if fn is nil — installing a nil callback would compile but crash
// on the first state transition.
func WithCBOnStateChange(fn func(from, to circuitbreaker.State)) ResilientOption {
	if fn == nil {
		panic("httpx: WithCBOnStateChange requires a non-nil callback")
	}
	return func(c *resilientConfig) { c.onStateChange = fn }
}

// WithHostCircuitBreakers isolates circuit-breaker state per request host
// (req.URL.Host). Use when a single *http.Client fans out to multiple
// downstreams and one failing host must not fail-fast the others.
//
// Without this option the client shares one breaker across all hosts — the
// safer default when the client is scoped to a single dependency.
func WithHostCircuitBreakers() ResilientOption {
	return func(c *resilientConfig) { c.hostCircuitBreakers = true }
}

// WithDeadlineBudget enables deadline budget propagation. When the caller's
// context has a deadline, the outbound request timeout is derived from the
// remaining budget minus a safety margin, instead of using the static client
// timeout. The deadline transport is outermost in the transport chain so it
// adjusts the context before the circuit breaker evaluates.
//
// Note: the static http.Client.Timeout (default 10s) still applies as an
// upper bound. If the deadline budget exceeds the client timeout, the client
// timeout wins. To rely solely on deadline budget propagation, use
// [WithoutResilientTimeout].
func WithDeadlineBudget(opts ...DeadlineBudgetOption) ResilientOption {
	copied := append([]DeadlineBudgetOption(nil), opts...)
	return func(c *resilientConfig) {
		c.deadlineBudget = true
		c.deadlineBudgetCfg = deadlineBudgetConfig{
			safetyMargin: defaultSafetyMargin,
			minTimeout:   defaultMinTimeout,
		}
		for _, o := range copied {
			if o == nil {
				panic("httpx: WithDeadlineBudget option must not be nil")
			}
			o(&c.deadlineBudgetCfg)
		}
	}
}

// NewResilientHTTPClient returns an *http.Client with a circuit-breaker-protected
// transport. When the downstream dependency fails repeatedly (transport errors
// or 5xx responses), the circuit opens and requests fail fast with
// [circuitbreaker.ErrCircuitOpen] instead of waiting for timeouts.
//
// The transport is cloned from http.DefaultTransport to inherit production
// defaults. By default a single circuit breaker protects every host the
// client contacts — create one resilient client per downstream dependency,
// or pass [WithHostCircuitBreakers] for per-host isolation.
//
// For retry logic, use [retry.Do] at the call site — retrying at the transport
// level is unsafe because request bodies are consumed on the first attempt.
func NewResilientHTTPClient(opts ...ResilientOption) *http.Client {
	cfg := resilientConfig{
		timeout:     10 * time.Second,
		cbThreshold: 5,
		cbReset:     30 * time.Second,
		shouldTrip:  defaultShouldTrip,
	}
	for _, o := range opts {
		if o == nil {
			panic("httpx: NewResilientHTTPClient option must not be nil")
		}
		o(&cfg)
	}

	transport := newKitTransportWithLabel(cfg.tlsConfig, clientConfig{
		idleConnTimeout: cfg.idleConnTimeout,
	}, "httpx: NewResilientHTTPClient")

	var stateChange func(from, to circuitbreaker.State)
	if cfg.onStateChange != nil {
		stateChange = cfg.onStateChange
	}

	var rt http.RoundTripper = newCircuitBreakerTransport(
		transport, cfg.shouldTrip, cfg.cbThreshold, cfg.cbReset, stateChange, cfg.hostCircuitBreakers,
	)

	if cfg.deadlineBudget {
		rt = &deadlineBudgetTransport{
			base:         rt,
			safetyMargin: cfg.deadlineBudgetCfg.safetyMargin,
			minTimeout:   cfg.deadlineBudgetCfg.minTimeout,
		}
	}

	return &http.Client{
		Timeout:       cfg.timeout,
		Transport:     rt,
		CheckRedirect: redirectPolicyOrDefault(cfg.checkRedirect),
	}
}

// newCircuitBreakerTransport bundles the circuit breaker construction with the
// transport so the breaker's IsSuccessful predicate always recognises the
// notCountedError sentinel. Keeping both together guarantees a shouldTrip
// predicate that excludes an error (returns false for a non-nil error) actually
// prevents that error from advancing the failure threshold.
func newCircuitBreakerTransport(
	base http.RoundTripper,
	shouldTrip func(resp *http.Response, err error) bool,
	threshold int,
	reset time.Duration,
	onStateChange func(from, to circuitbreaker.State),
	perHost bool,
) *circuitBreakerTransport {
	var cbOpts []circuitbreaker.Option
	if onStateChange != nil {
		cbOpts = append(cbOpts, circuitbreaker.WithOnStateChange(func(_ string, from, to circuitbreaker.State) {
			onStateChange(from, to)
		}))
	}
	// Only nil is a success. Predicate-excluded transport errors are wrapped
	// as notCountedError and passed to IsExcluded so they neither trip the
	// breaker nor reset consecutive-failure progress / heal half-open.
	cbOpts = append(cbOpts, circuitbreaker.WithIsSuccessful(func(err error) bool {
		return err == nil
	}))
	cbOpts = append(cbOpts, circuitbreaker.WithIsExcluded(func(err error) bool {
		var nc *notCountedError
		return errors.As(err, &nc)
	}))

	t := &circuitBreakerTransport{
		base:       base,
		shouldTrip: shouldTrip,
		threshold:  threshold,
		reset:      reset,
		cbOpts:     cbOpts,
		perHost:    perHost,
	}
	if perHost {
		t.breakers = make(map[string]*circuitbreaker.CircuitBreaker)
	} else {
		t.cb = circuitbreaker.NewCircuitBreaker(threshold, reset, cbOpts...)
	}
	return t
}

// circuitBreakerTransport wraps an http.RoundTripper with circuit breaker logic.
type circuitBreakerTransport struct {
	base       http.RoundTripper
	cb         *circuitbreaker.CircuitBreaker
	shouldTrip func(resp *http.Response, err error) bool

	// Per-host isolation (WithHostCircuitBreakers).
	perHost   bool
	threshold int
	reset     time.Duration
	cbOpts    []circuitbreaker.Option
	mu        sync.Mutex
	breakers  map[string]*circuitbreaker.CircuitBreaker
}

func (t *circuitBreakerTransport) breakerFor(req *http.Request) *circuitbreaker.CircuitBreaker {
	if !t.perHost {
		return t.cb
	}
	host := ""
	if req != nil && req.URL != nil {
		host = req.URL.Host
	}
	if host == "" {
		host = "_"
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if cb, ok := t.breakers[host]; ok {
		return cb
	}
	cb := circuitbreaker.NewCircuitBreaker(t.threshold, t.reset, t.cbOpts...)
	t.breakers[host] = cb
	return cb
}

// RoundTrip executes the request through the circuit breaker. If the circuit
// is open, it returns circuitbreaker.ErrCircuitOpen immediately.
func (t *circuitBreakerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	var resp *http.Response
	var executed bool
	err := t.breakerFor(req).Execute(func() error {
		executed = true
		var rtErr error
		resp, rtErr = t.base.RoundTrip(req)
		if t.shouldTrip(resp, rtErr) {
			if rtErr != nil {
				return rtErr
			}
			// Return a sentinel error so the circuit breaker counts this as
			// a failure, but we still return the actual response to the caller.
			return &serverError{code: resp.StatusCode}
		}
		if rtErr != nil {
			// The predicate excluded this error from the failure count. Wrap it
			// in a non-counting sentinel so the breaker's IsExcluded predicate
			// ignores it; it is unwrapped before returning so the caller still
			// observes the original error.
			return &notCountedError{err: rtErr}
		}
		return nil
	})

	// The circuit rejected the call without invoking the closure (open or
	// half-open over the probe limit). Per the http.RoundTripper contract the
	// request body must still be closed, since base.RoundTrip never ran.
	if !executed {
		if req.Body != nil {
			_ = req.Body.Close()
		}
		return nil, err
	}

	// A predicate-excluded transport error was wrapped to avoid counting it.
	// Unwrap so the caller observes the original error, with no response.
	var nc *notCountedError
	if errors.As(err, &nc) {
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
		return nil, nc.err
	}

	// If the circuit tripped on a 5xx, the actual response is still valid.
	// Return it so the caller can inspect the status code and read the body.
	// The nil error means the caller handles 5xx via resp.StatusCode — this
	// follows the net/http convention where non-2xx is not an error.
	// The circuit breaker has already recorded this as a failure internally.
	if resp != nil && err != nil {
		if _, ok := err.(*serverError); ok {
			return resp, nil
		}
		// Non-serverError with a response: close body to prevent leak.
		// Per RoundTripper contract, when err != nil, resp should be nil.
		if resp.Body != nil {
			_ = resp.Body.Close()
		}
		return nil, err
	}
	return resp, err
}

// serverError is a sentinel used to signal the circuit breaker that a server
// error occurred, while still allowing the response to be returned to callers.
type serverError struct {
	code int
}

func (e *serverError) Error() string {
	return http.StatusText(e.code)
}

// notCountedError wraps a transport error that the shouldTrip predicate
// explicitly excluded from the failure count. The breaker's IsExcluded
// predicate recognises it so it neither advances the failure threshold nor
// resets consecutive failures; RoundTrip unwraps it before returning to the
// caller.
type notCountedError struct {
	err error
}

func (e *notCountedError) Error() string { return e.err.Error() }

func (e *notCountedError) Unwrap() error { return e.err }

// defaultShouldTrip is the default circuit-breaker failure predicate for
// [NewResilientHTTPClient]. Transport errors and HTTP 5xx responses count
// toward the failure threshold, with the exception of caller-driven
// cancellation (context.Canceled): a client aborting an in-flight request
// is not evidence the downstream is unhealthy, so a flood of cancelled
// requests must not open the breaker and harm unrelated callers. Server-side
// timeouts (context.DeadlineExceeded) still count as failures.
func defaultShouldTrip(resp *http.Response, err error) bool {
	if err != nil {
		return !errors.Is(err, context.Canceled)
	}
	return resp != nil && resp.StatusCode >= 500
}
