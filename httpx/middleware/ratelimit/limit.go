package ratelimit

import (
	"errors"
	"net"
	"net/http"
	"strconv"
	"time"

	limiter "github.com/bds421/rho-kit/data/v2/ratelimit"
	"github.com/bds421/rho-kit/httpx/v2"
)

// unavailableRetryAfter is the pause a client takes while the limiter itself
// fails, short enough to ride out a Redis failover.
const unavailableRetryAfter = 5 * time.Second

// Limit admits requests under one [limiter.Limiter], a budget per key:
// an in-memory limiter on a single instance, a Redis one across replicas,
// or a [limiter.Fallback] of both. Every answer carries the RateLimit-Policy
// field; a refusal carries Retry-After and the RateLimit field with the
// time until the budget admits a request again.
type Limit struct {
	Limiter limiter.Limiter
	Policy  Policy
	// Keys names the budgets a request draws on, every one of which must
	// admit it; nil draws on the client address (the host of RemoteAddr,
	// which the clientip middleware sets to the client behind trusted
	// proxies).
	Keys func(r *http.Request) []string
	// Refuse writes the 429 once the fields are set; nil writes the kit's
	// JSON error. Unavailable writes the 503 of a failing limiter once
	// Retry-After is set; nil writes the kit's JSON error.
	Refuse      func(w http.ResponseWriter, r *http.Request)
	Unavailable func(w http.ResponseWriter, r *http.Request)
}

// Middleware admits each request under the limit before next sees it.
func (l Limit) Middleware(next http.Handler) http.Handler {
	if l.Limiter == nil {
		panic("middleware/ratelimit: Limit requires a Limiter")
	}
	if !l.Policy.valid() {
		panic("middleware/ratelimit: Limit requires a Policy with a name, a quota and a window")
	}
	if next == nil {
		panic("middleware/ratelimit: Limit requires a non-nil next handler")
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		SetPolicy(w.Header(), l.Policy)
		for _, key := range l.keys(r) {
			allowed, retryAfter, err := l.Limiter.Allow(r.Context(), key)
			switch {
			case errors.Is(err, limiter.ErrInvalidKey):
				httpx.WriteError(w, http.StatusBadRequest, "invalid rate limit key")
				return
			case err != nil:
				w.Header().Set("Retry-After", strconv.FormatInt(seconds(unavailableRetryAfter), 10))
				l.unavailable(w, r)
				return
			case !allowed:
				w.Header().Set("Retry-After", strconv.FormatInt(seconds(retryAfter), 10))
				SetRemaining(w.Header(), l.Policy, 0, retryAfter)
				l.refuse(w, r)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (l Limit) keys(r *http.Request) []string {
	if l.Keys != nil {
		return l.Keys(r)
	}
	return []string{ClientAddress(r)}
}

// ClientAddress is the host of the request's RemoteAddr, the default key.
func ClientAddress(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (l Limit) refuse(w http.ResponseWriter, r *http.Request) {
	if l.Refuse != nil {
		l.Refuse(w, r)
		return
	}
	httpx.WriteError(w, http.StatusTooManyRequests, "rate limit exceeded")
}

func (l Limit) unavailable(w http.ResponseWriter, r *http.Request) {
	if l.Unavailable != nil {
		l.Unavailable(w, r)
		return
	}
	httpx.WriteError(w, http.StatusServiceUnavailable, "rate limit unavailable")
}
