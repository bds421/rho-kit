package httpx

import (
	"crypto/tls"
	"net/http"
	"time"

	"github.com/bds421/rho-kit/resilience/circuitbreaker"
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
	deadlineBudget    bool
	deadlineBudgetCfg deadlineBudgetConfig
}

// WithResilientTimeout sets the HTTP client timeout. Default: 10s.
func WithResilientTimeout(d time.Duration) ResilientOption {
	return func(c *resilientConfig) { c.timeout = d }
}

// WithResilientTLS sets the TLS configuration for the transport.
func WithResilientTLS(cfg *tls.Config) ResilientOption {
	return func(c *resilientConfig) { c.tlsConfig = cfg }
}

// WithCBThreshold sets the consecutive failure count to trip the circuit
// breaker. Default: 5.
func WithCBThreshold(n int) ResilientOption {
	return func(c *resilientConfig) {
		if n > 0 {
			c.cbThreshold = n
		}
	}
}

// WithCBResetTimeout sets how long the circuit stays open before allowing
// a probe request. Default: 30s.
func WithCBResetTimeout(d time.Duration) ResilientOption {
	return func(c *resilientConfig) { c.cbReset = d }
}

// WithCBShouldTrip sets a custom predicate for deciding whether a response/error
// should count toward the circuit breaker failure threshold. By default,
// transport errors and HTTP 5xx responses trip the breaker.
func WithCBShouldTrip(fn func(resp *http.Response, err error) bool) ResilientOption {
	return func(c *resilientConfig) { c.shouldTrip = fn }
}

// WithCBOnStateChange registers a callback for circuit breaker state transitions.
func WithCBOnStateChange(fn func(from, to circuitbreaker.State)) ResilientOption {
	return func(c *resilientConfig) { c.onStateChange = fn }
}

// WithDeadlineBudget enables deadline budget propagation. When the caller's
// context has a deadline, the outbound request timeout is derived from the
// remaining budget minus a safety margin, instead of using the static client
// timeout. The deadline transport is outermost in the transport chain so it
// adjusts the context before the circuit breaker evaluates.
func WithDeadlineBudget(opts ...DeadlineBudgetOption) ResilientOption {
	return func(c *resilientConfig) {
		c.deadlineBudget = true
		c.deadlineBudgetCfg = deadlineBudgetConfig{
			safetyMargin: defaultSafetyMargin,
			minTimeout:   defaultMinTimeout,
		}
		for _, o := range opts {
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
// defaults. The circuit breaker wraps the transport layer — all requests
// through this client are protected.
//
// For retry logic, use [retry.Do] at the call site — retrying at the transport
// level is unsafe because request bodies are consumed on the first attempt.
func NewResilientHTTPClient(opts ...ResilientOption) *http.Client {
	cfg := resilientConfig{
		timeout:     10 * time.Second,
		cbThreshold: 5,
		cbReset:     30 * time.Second,
		shouldTrip: func(resp *http.Response, err error) bool {
			if err != nil {
				return true
			}
			return resp != nil && resp.StatusCode >= 500
		},
	}
	for _, o := range opts {
		o(&cfg)
	}

	transport := http.DefaultTransport.(*http.Transport).Clone()
	if cfg.tlsConfig != nil {
		transport.TLSClientConfig = cfg.tlsConfig
	}

	var cbOpts []circuitbreaker.Option
	if cfg.onStateChange != nil {
		cbOpts = append(cbOpts, circuitbreaker.WithOnStateChange(func(_ string, from, to circuitbreaker.State) {
			cfg.onStateChange(from, to)
		}))
	}
	cbOpts = append(cbOpts, circuitbreaker.WithIsSuccessful(func(err error) bool {
		// Errors from the circuit-breaker transport are either transport
		// errors (non-nil err) or sentinel serverError (5xx). Both are
		// reported as failures. All other outcomes are successes.
		return err == nil
	}))

	cb := circuitbreaker.NewCircuitBreaker(cfg.cbThreshold, cfg.cbReset, cbOpts...)

	var rt http.RoundTripper = &circuitBreakerTransport{
		base:       transport,
		cb:         cb,
		shouldTrip: cfg.shouldTrip,
	}

	if cfg.deadlineBudget {
		rt = &deadlineBudgetTransport{
			base:         rt,
			safetyMargin: cfg.deadlineBudgetCfg.safetyMargin,
			minTimeout:   cfg.deadlineBudgetCfg.minTimeout,
		}
	}

	return &http.Client{
		Timeout:   cfg.timeout,
		Transport: rt,
	}
}

// circuitBreakerTransport wraps an http.RoundTripper with circuit breaker logic.
type circuitBreakerTransport struct {
	base       http.RoundTripper
	cb         *circuitbreaker.CircuitBreaker
	shouldTrip func(resp *http.Response, err error) bool
}

// RoundTrip executes the request through the circuit breaker. If the circuit
// is open, it returns circuitbreaker.ErrCircuitOpen immediately.
func (t *circuitBreakerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	var resp *http.Response
	err := t.cb.Execute(func() error {
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
		return rtErr
	})

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
