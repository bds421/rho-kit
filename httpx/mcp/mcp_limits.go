package mcp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/bds421/rho-kit/core/v2/apperror"
	"github.com/bds421/rho-kit/httpx/v2/middleware/ratelimit"
)

// CallLimit decides whether one more operation of a tool call may run, for
// the caller of the MCP request r: allowed, or not and when to try again.
// An error means the limit could not be asked; the operation is refused.
type CallLimit func(r *http.Request) (allowed bool, retryAfter time.Duration, err error)

// ErrCallLimited is the refusal of [Charge]: a [CallLimit] said no. It is
// a rate-limit error (apperror), so a handler that returns it unchanged
// answers "rate limit exceeded".
var ErrCallLimited = apperror.NewRateLimit("mcp: the caller is over its rate limit")

// CallLimitedError is what [Charge] returns when a limit refuses: it
// matches [ErrCallLimited] and says when to try again.
type CallLimitedError struct{ RetryAfter time.Duration }

func (e *CallLimitedError) Error() string {
	return fmt.Sprintf("mcp: the caller is over its rate limit; try again in %s", e.RetryAfter.Round(time.Second))
}

func (e *CallLimitedError) Unwrap() error { return ErrCallLimited }

// WithCallLimits makes every operation a tool handler runs inside one
// call count against the caller's rate limits: the handler calls [Charge]
// before each, which asks every limit with the MCP request. An HTTP
// middleware limit counts the MCP request once; this counts what the
// call does inside the service. Use [LimitByIP] and [LimitByKey] for the
// kit's limiters.
func WithCallLimits(limits ...CallLimit) ServerOption {
	for _, l := range limits {
		if l == nil {
			panic("mcp: WithCallLimits requires non-nil limits")
		}
	}
	cloned := append([]CallLimit(nil), limits...)
	return func(c *serverConfig) { c.callLimits = append(c.callLimits, cloned...) }
}

type callKey struct{}

// callScope is what Charge needs of the MCP request a tool call came in.
type callScope struct {
	r      *http.Request
	limits []CallLimit
}

// withCallScope puts r and the server's limits on r's context, for Charge.
func (s *Server) withCallScope(next http.Handler) http.Handler {
	if len(s.cfg.callLimits) == 0 {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		scope := &callScope{r: r, limits: s.cfg.callLimits}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), callKey{}, scope)))
	})
}

// Charge counts one operation of the tool call ctx belongs to against
// every limit of [WithCallLimits]. It returns a [*CallLimitedError]
// (matching ErrCallLimited) when one refuses, the limit's error when one
// fails, and nil when all allow or the server has none.
func Charge(ctx context.Context) error {
	scope, ok := ctx.Value(callKey{}).(*callScope)
	if !ok {
		return nil
	}
	for _, limit := range scope.limits {
		allowed, retryAfter, err := limit(scope.r)
		if err != nil {
			return fmt.Errorf("mcp: rate limit: %w", err)
		}
		if !allowed {
			return &CallLimitedError{RetryAfter: retryAfter}
		}
	}
	return nil
}

// LimitByIP counts an operation against the per-IP limiter, keyed by the
// client address as its middleware derives it (trusted proxies included).
func LimitByIP(l *ratelimit.Limiter) CallLimit {
	if l == nil {
		panic("mcp: LimitByIP requires a limiter")
	}
	return func(r *http.Request) (bool, time.Duration, error) {
		ip := l.ClientIP(r)
		if ip == "" {
			return false, 0, errors.New("client IP could not be determined")
		}
		return l.Allow(r.Context(), ip)
	}
}

// LimitByKey counts an operation against a keyed limiter under the key
// key derives from the request (an API key's id); a request without one
// is refused.
func LimitByKey(l *ratelimit.KeyedLimiter, key func(*http.Request) (string, bool)) CallLimit {
	if l == nil || key == nil {
		panic("mcp: LimitByKey requires a limiter and a key function")
	}
	return func(r *http.Request) (bool, time.Duration, error) {
		k, ok := key(r)
		if !ok || k == "" {
			return false, 0, errors.New("the request names no rate-limit key")
		}
		return l.Allow(r.Context(), k)
	}
}
