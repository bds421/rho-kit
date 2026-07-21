// asvs: V2.2.1, V11.1.1
package ratelimit

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"log/slog"
	"math"
	"net"
	"net/http"
	"sync"
	"time"

	lru "github.com/hashicorp/golang-lru/v2"

	"github.com/bds421/rho-kit/core/v2/clock"
	"github.com/bds421/rho-kit/core/v2/redact"
	"github.com/bds421/rho-kit/httpx/v2"
	"github.com/bds421/rho-kit/httpx/v2/middleware/clientip"
)

const numShards = 16

const maxDurationValue = time.Duration(1<<63 - 1)

type visitor struct {
	count    int
	windowAt time.Time
}

type shard struct {
	mu       sync.Mutex
	visitors *lru.Cache[string, *visitor]
}

// defaultMaxPerShard limits each shard's LRU size to prevent OOM from IP-spray attacks.
// When the LRU is full, admitting a new key silently evicts the least-recently-used
// counter (including the attacker's own), which resets that key's rate limit —
// fail-open under memory pressure. Operators should alert on
// http_ratelimit_lru_evictions_total and use a Redis-backed limiter for adversarial
// surfaces (login, OTP). The cap itself is intentional: OOM is worse than a
// temporary limit bypass.
const defaultMaxPerShard = 10_000

// Limiter is a sharded fixed-window rate limiter keyed by IP address.
// Sharding reduces mutex contention under high concurrency. The type
// satisfies [lifecycle.Component] so callers can register it directly with
// a lifecycle.Runner.
//
// Concurrency: Allow is safe for concurrent use. Start must be invoked
// from a single goroutine — the lifecycle.Runner provides this contract.
type Limiter struct {
	shards         [numShards]shard
	limit          int
	window         time.Duration
	now            clock.Func
	trustedProxies []*net.IPNet
	maxPerShard    int
	health         HealthIndicator
	degradation    DegradationHandler
	metrics        *Metrics
	name           string

	startMu sync.Mutex
	started bool
	stopped bool
	cancel  context.CancelFunc
	doneCh  chan struct{}
}

// LimiterOption configures optional Limiter behaviour.
type LimiterOption func(*Limiter)

// WithClock sets a custom time source (useful for testing). Panics on
// nil to fail fast at construction rather than dereferencing a nil
// func on the first request through the limiter.
func WithClock(fn clock.Func) LimiterOption {
	if fn == nil {
		panic("middleware/ratelimit: WithClock requires a non-nil time source")
	}
	return func(rl *Limiter) { rl.now = fn }
}

// WithTrustedProxies sets the CIDRs from which X-Forwarded-For is trusted.
// Invalid entries panic so proxy attribution cannot silently degrade at startup.
//
// A nil or empty slice falls back to the default trusted set (loopback) rather
// than "trust no proxies" — passing []string{} does NOT disable XFF trust.
// This preserves the kit's default attribution behavior; to trust no proxies,
// configure the limiter behind an environment where XFF is already stripped.
func WithTrustedProxies(cidrs []string) LimiterOption {
	trusted, err := clientip.ParseTrustedProxiesStrict(cidrs)
	if err != nil {
		// Surface which entry is malformed (index only) so operators can fix
		// the config; the raw CIDR value is withheld for secret hygiene.
		panic(fmt.Sprintf("middleware/ratelimit: WithTrustedProxies invalid trusted proxy at index %d", invalidTrustedProxyIndex(cidrs)))
	}
	if len(trusted) == 0 {
		trusted = clientip.ParseTrustedProxies(nil)
	}
	return func(rl *Limiter) {
		rl.trustedProxies = cloneIPNets(trusted)
	}
}

// invalidTrustedProxyIndex returns the index of the first CIDR entry that
// ParseTrustedProxiesStrict rejects, or -1 if none is individually invalid
// (e.g. a whole-slice failure mode). The entry value is never returned, only
// its position, so no potentially sensitive config leaks into the panic.
func invalidTrustedProxyIndex(cidrs []string) int {
	for i, c := range cidrs {
		if _, err := clientip.ParseTrustedProxiesStrict([]string{c}); err != nil {
			return i
		}
	}
	return -1
}

// WithMetrics attaches Prometheus metrics to the IP rate limiter.
func WithMetrics(m *Metrics) LimiterOption {
	if m == nil {
		panic("middleware/ratelimit: WithMetrics requires non-nil metrics")
	}
	return func(rl *Limiter) { rl.metrics = m }
}

// WithLimiterName sets the low-cardinality limiter label used by Prometheus
// metrics. Use static names such as "public_api" or "login".
func WithLimiterName(name string) LimiterOption {
	name = normalizeLimiterName(name)
	return func(rl *Limiter) { rl.name = name }
}

// NewLimiter creates a rate limiter that allows limit requests per window per IP.
// Panics if limit or window are not positive — these indicate misconfiguration.
func NewLimiter(limit int, window time.Duration, opts ...LimiterOption) *Limiter {
	if limit <= 0 {
		panic("middleware/ratelimit: NewLimiter limit must be positive")
	}
	if window <= 0 {
		panic("middleware/ratelimit: NewLimiter window must be positive")
	}
	rl := &Limiter{
		limit:       limit,
		window:      window,
		now:         time.Now,
		maxPerShard: defaultMaxPerShard,
		name:        defaultLimiterName,
	}
	for _, opt := range opts {
		if opt == nil {
			panic("middleware/ratelimit: NewLimiter option must not be nil")
		}
		opt(rl)
	}
	if len(rl.trustedProxies) == 0 {
		rl.trustedProxies = clientip.ParseTrustedProxies(nil)
	}
	for i := range rl.shards {
		// IMPORTANT: Do NOT use lru.NewWithEvict here. The shard mutex is held
		// during Add() calls, and an OnEvict callback would execute under that
		// same lock, risking deadlock if it touches any shard state.
		cache, _ := lru.New[string, *visitor](rl.maxPerShard)
		rl.shards[i].visitors = cache
	}
	return rl
}

func cloneIPNets(in []*net.IPNet) []*net.IPNet {
	out := make([]*net.IPNet, 0, len(in))
	for _, n := range in {
		if n == nil {
			out = append(out, nil)
			continue
		}
		out = append(out, &net.IPNet{
			IP:   append(net.IP(nil), n.IP...),
			Mask: append(net.IPMask(nil), n.Mask...),
		})
	}
	return out
}

func (rl *Limiter) ready() error {
	if rl == nil || rl.limit <= 0 || rl.window <= 0 || rl.now == nil {
		return ErrInvalidLimiter
	}
	for i := range rl.shards {
		if rl.shards[i].visitors == nil {
			return ErrInvalidLimiter
		}
	}
	return nil
}

// getShard returns the shard for the given IP using FNV-1a hashing.
func (rl *Limiter) getShard(ip string) *shard {
	h := fnv.New32a()
	h.Write([]byte(ip))
	return &rl.shards[h.Sum32()%numShards]
}

// allow checks if the IP is within the rate limit. Returns (allowed, windowRemaining).
// windowRemaining is only meaningful when allowed is false.
func (rl *Limiter) allow(ip string) (bool, time.Duration) {
	if rl.ready() != nil {
		rl.observeDecision(rateLimitOutcomeUnavailable)
		return false, 0
	}
	if ip == "" {
		rl.observeDecision(rateLimitOutcomeInvalidClientIP)
		return false, 0
	}
	s := rl.getShard(ip)
	s.mu.Lock()
	defer s.mu.Unlock()

	now := rl.now()
	v, ok := s.visitors.Get(ip)
	if ok {
		// Mutating *visitor through the cached pointer is safe: the shard mutex
		// serialises all access. Replacing via Add() would be wasteful since
		// Get() already marked this entry as recently used.
		elapsed := now.Sub(v.windowAt)
		if elapsed >= rl.window {
			v.count = 1
			v.windowAt = now
			rl.observeDecision(rateLimitOutcomeAllowed)
			return true, 0
		}
		v.count++
		if v.count <= rl.limit {
			rl.observeDecision(rateLimitOutcomeAllowed)
			return true, 0
		}
		remaining := rl.window - elapsed
		if remaining < 0 {
			remaining = 0
		}
		rl.observeDecision(rateLimitOutcomeLimited)
		return false, remaining
	}

	if s.visitors.Add(ip, &visitor{count: 1, windowAt: now}) {
		rl.observeLRUEviction()
	}
	rl.observeDecision(rateLimitOutcomeAllowed)
	return true, 0
}

func (rl *Limiter) observeLRUEviction() {
	if rl == nil || rl.metrics == nil {
		return
	}
	rl.metrics.observeLRUEviction(rl.name, "ip")
}

// maxCleanupPerShard limits the number of keys scanned per shard per cleanup
// cycle to prevent large allocations from Keys() under IP-spray attacks.
const maxCleanupPerShard = 1000

// cleanup is a best-effort hint that scans up to [maxCleanupPerShard] keys
// per shard and evicts those whose window has expired. Real GC under load
// is the LRU eviction inside `s.visitors`; cleanup just keeps the working
// set small between bursts.
//
// The two-phase Keys/Peek-Remove pattern is intentional: Keys() snapshots
// the shard's key set, then the per-key Peek-Remove re-checks each entry's
// window. Both phases run under the shard lock, so the O(n) Keys()
// snapshot does briefly serialize with concurrent allow() calls. The
// per-key Peek-Remove is racy in theory (a visitor could be re-touched
// between snapshot and Peek), but Peek doesn't trigger LRU promotion so
// the re-check is benign and a freshly-touched entry stays even if it was
// stale at snapshot time.
func (rl *Limiter) cleanup() {
	if rl.ready() != nil {
		return
	}
	cutoff := rl.now().Add(-rl.window)
	for i := range rl.shards {
		s := &rl.shards[i]
		// Snapshot keys under the shard lock. The O(n) Keys()
		// allocation briefly serializes with concurrent allow() calls.
		s.mu.Lock()
		keys := s.visitors.Keys()
		s.mu.Unlock()

		limit := min(len(keys), maxCleanupPerShard)
		s.mu.Lock()
		for _, ip := range keys[:limit] {
			v, ok := s.visitors.Peek(ip)
			if ok && v.windowAt.Before(cutoff) {
				s.visitors.Remove(ip)
			}
		}
		s.mu.Unlock()
	}
}

// Name returns the limiter's configured name so the type satisfies the
// [lifecycle.Component]-adjacent naming convention used by the Runner.
func (rl *Limiter) Name() string {
	if rl == nil || rl.name == "" {
		return defaultLimiterName
	}
	return rl.name
}

// Start launches the periodic cleanup goroutine and blocks until ctx is
// cancelled OR [Limiter.Stop] is invoked. Cleanup runs at 2× the rate
// limit window to amortize scan cost while ensuring expired entries don't
// accumulate beyond one extra window. Start must only be called once;
// subsequent calls return an error.
func (rl *Limiter) Start(ctx context.Context) error {
	if err := rl.ready(); err != nil {
		return err
	}
	if ctx == nil {
		return errors.New("ratelimit: Limiter.Start requires a non-nil context")
	}
	rl.startMu.Lock()
	if rl.started {
		rl.startMu.Unlock()
		return errors.New("ratelimit: Limiter.Start already started")
	}
	if rl.stopped {
		// Stop ran before Start and latched stopped=true. Launching the
		// cleanup loop now would orphan a goroutine the prior Stop already
		// promised to wait on. Reject, mirroring lifecycle.FuncComponent.
		rl.startMu.Unlock()
		return errors.New("ratelimit: Limiter.Start already stopped")
	}
	rl.started = true
	runCtx, cancel := context.WithCancel(ctx)
	rl.cancel = cancel
	done := make(chan struct{})
	rl.doneCh = done
	rl.startMu.Unlock()

	defer close(done)
	defer cancel()

	ticker := time.NewTicker(cleanupInterval(rl.window))
	defer ticker.Stop()
	for {
		select {
		case <-runCtx.Done():
			return nil
		case <-ticker.C:
			func() {
				defer func() {
					if r := recover(); r != nil {
						slog.Error("panic in rate limiter cleanup",
							slog.String("limiter", rl.Name()),
							redact.Panic(r),
						)
					}
				}()
				rl.cleanup()
			}()
		}
	}
}

// Stop cancels the cleanup goroutine launched by [Limiter.Start] and
// waits for it to exit. Stop is idempotent; calls before Start, after the
// goroutine has already exited, or after a prior Stop are no-ops.
func (rl *Limiter) Stop(ctx context.Context) error {
	if rl == nil {
		return nil
	}
	rl.startMu.Lock()
	if !rl.started || rl.stopped {
		rl.stopped = true
		rl.startMu.Unlock()
		return nil
	}
	rl.stopped = true
	cancel := rl.cancel
	done := rl.doneCh
	rl.startMu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done == nil {
		return nil
	}
	if ctx == nil {
		<-done
		return nil
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func cleanupInterval(window time.Duration) time.Duration {
	if window > maxDurationValue/2 {
		return maxDurationValue
	}
	return window * 2
}

// clientIP extracts the real client IP using proxy-aware logic.
func (rl *Limiter) clientIP(r *http.Request) string {
	if rl.ready() != nil {
		return ""
	}
	return clientip.ClientIPWithTrustedProxies(r, rl.trustedProxies)
}

// ClientIP extracts the real client IP from the request, using the same
// proxy-aware logic as the rate limiter middleware.
func (rl *Limiter) ClientIP(r *http.Request) string {
	return rl.clientIP(r)
}

// Middleware returns a chain-shape HTTP middleware that rejects requests
// exceeding rl's rate limit. When degradation is configured via
// [WithDegradation], the middleware checks the health indicator before
// enforcing rate limits.
func Middleware(rl *Limiter) func(http.Handler) http.Handler {
	if rl == nil {
		panic("middleware/ratelimit: Middleware requires a non-nil Limiter")
	}
	if err := rl.ready(); err != nil {
		panic("middleware/ratelimit: Middleware requires an initialized limiter")
	}
	return func(next http.Handler) http.Handler {
		if next == nil {
			panic("middleware/ratelimit: Middleware requires a non-nil next handler")
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			skip, handled, outcome := handleDegradation(w, r, rl.health, rl.degradation)
			if outcome != "" {
				rl.observeDecision(outcome)
			}
			if handled {
				return
			}
			if skip {
				next.ServeHTTP(w, r)
				return
			}

			ip := rl.clientIP(r)
			if ip == "" {
				rl.observeDecision(rateLimitOutcomeInvalidClientIP)
				httpx.WriteError(w, http.StatusBadRequest, "client IP could not be determined")
				return
			}
			allowed, remaining := rl.allow(ip)
			if !allowed {
				retryAfter := int(math.Ceil(remaining.Seconds()))
				if retryAfter < 1 {
					retryAfter = 1
				}
				rl.observeRetryAfter(float64(retryAfter))
				w.Header().Set("Retry-After", fmt.Sprintf("%d", retryAfter))
				httpx.WriteError(w, http.StatusTooManyRequests, "rate limit exceeded")
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

func (rl *Limiter) observeDecision(outcome string) {
	if rl == nil {
		return
	}
	rl.metrics.observeDecision(rl.name, rateLimitKindIP, outcome)
}

func (rl *Limiter) observeRetryAfter(seconds float64) {
	if rl == nil {
		return
	}
	rl.metrics.observeRetryAfter(rl.name, rateLimitKindIP, seconds)
}
