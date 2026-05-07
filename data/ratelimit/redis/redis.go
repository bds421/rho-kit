// Package redis implements a Redis-backed GCRA [ratelimit.Limiter].
//
// State per key is a single int64 nanosecond timestamp ("theoretical
// arrival time"). Each Allow call is an atomic Lua script that loads,
// updates, and expires the key in one round trip. Across many app
// replicas the limit is the same: a Redis-backed GCRA is the
// algorithm-agnostic equivalent of swapping the in-memory map for a
// shared store.
//
// Use this when:
//
//   - The same per-key budget must apply across multiple replicas
//     (per-tenant API quotas, abuse limits at the edge).
//   - You can tolerate one Redis round trip per gated request.
//
// Compared to in-memory variants the trade-offs are:
//
//   - Latency: ~0.5–2 ms per call (typical Redis); in-memory is ns.
//   - Availability: a Redis outage stalls every gated request unless
//     the caller wraps the limiter in a fail-open/fail-closed policy.
//     The package does NOT silently fail open — it surfaces the Redis
//     error so the caller can decide.
//
// Time source: Allow takes the local time as the GCRA reference. For
// strict cross-replica fairness in the presence of clock skew, set
// [WithRedisTime] to use Redis's TIME command (one extra round trip,
// or pipeline it via your wrapper).
package redis

import (
	"context"
	"errors"
	"fmt"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/bds421/rho-kit/data/ratelimit"
)

// gcraScript is the atomic GCRA evaluation.
//
//	KEYS[1] = per-key state ("<prefix>:<key>")
//	ARGV[1] = now in nanoseconds (caller's clock)
//	ARGV[2] = rate (period / burst) in nanoseconds
//	ARGV[3] = burst (cells)
//	ARGV[4] = key TTL in seconds (>=1)
//
// Returns: {allowed (0|1), retryAfter ns when denied}.
var gcraScript = goredis.NewScript(`
local now    = tonumber(ARGV[1])
local rate   = tonumber(ARGV[2])
local burst  = tonumber(ARGV[3])
local ttl    = tonumber(ARGV[4])

local tat = tonumber(redis.call("GET", KEYS[1]))
if not tat or tat < now then
  tat = now
end
local allowAt = tat - burst * rate
if now <= allowAt then
  return {0, allowAt - now + 1}
end
local newTat = tat + rate
redis.call("SET", KEYS[1], newTat, "EX", ttl)
return {1, 0}
`)

// Limiter is a per-key Redis-backed GCRA [ratelimit.Limiter].
type Limiter struct {
	client goredis.UniversalClient
	prefix string
	period time.Duration
	burst  int
	rate   time.Duration
	keyTTL time.Duration
	now    func(ctx context.Context) (time.Time, error)
}

// Option configures a Limiter.
type Option func(*Limiter)

// WithKeyPrefix sets the prefix prepended to every Redis key. Default:
// "ratelimit:gcra:". Pick a unique prefix per logical limiter so two
// limiters with different (period, burst) configurations on the same
// Redis can't collide.
func WithKeyPrefix(p string) Option {
	return func(l *Limiter) { l.prefix = p }
}

// WithKeyTTL overrides the per-key expiration. Default: max(period,
// 60s). Bounding TTL keeps cold keys from accumulating in Redis.
//
// The TTL must be at least `period` — shorter and Redis can evict the
// state mid-window, defeating the cross-replica budget for that key.
func WithKeyTTL(d time.Duration) Option {
	return func(l *Limiter) { l.keyTTL = d }
}

// WithClock overrides the local clock used to compute `now`. Tests
// only.
func WithClock(now func() time.Time) Option {
	return func(l *Limiter) {
		l.now = func(_ context.Context) (time.Time, error) { return now(), nil }
	}
}

// WithRedisTime uses the Redis server's clock for the GCRA `now`
// reference. Costs one extra round trip per Allow but eliminates
// cross-replica clock-skew artefacts. Use when the limit must be
// strictly enforced (anti-abuse, per-tenant quota with billing
// implications).
func WithRedisTime() Option {
	return func(l *Limiter) {
		l.now = func(ctx context.Context) (time.Time, error) {
			t, err := l.client.Time(ctx).Result()
			if err != nil {
				return time.Time{}, fmt.Errorf("ratelimit/redis: TIME: %w", err)
			}
			return t, nil
		}
	}
}

// New constructs a Limiter that allows up to `burst` events within any
// `period` duration, smoothed at `period/burst` per event, persisted
// in `client`.
//
// Examples mirror the in-memory gcra package:
//
//   - New(client, time.Second, 10): 10 events/sec smoothed.
//   - New(client, time.Minute, 60): 60 events/min smoothed.
func New(client goredis.UniversalClient, period time.Duration, burst int, opts ...Option) *Limiter {
	if client == nil {
		panic("ratelimit/redis: client must not be nil")
	}
	if period <= 0 {
		panic("ratelimit/redis: period must be > 0")
	}
	if burst < 1 {
		panic("ratelimit/redis: burst must be >= 1")
	}
	rate := period / time.Duration(burst)
	l := &Limiter{
		client: client,
		prefix: "ratelimit:gcra:",
		period: period,
		burst:  burst,
		rate:   rate,
		keyTTL: maxDuration(period, time.Minute),
		now: func(_ context.Context) (time.Time, error) {
			return time.Now(), nil
		},
	}
	for _, o := range opts {
		o(l)
	}
	if l.keyTTL < period {
		panic("ratelimit/redis: key TTL must be >= period (otherwise Redis can evict mid-window)")
	}
	return l
}

// Allow reports whether key's next event is permitted. retryAfter is
// the time-until-next-allowed when denied.
func (l *Limiter) Allow(ctx context.Context, key string) (bool, time.Duration, error) {
	if key == "" {
		return false, 0, ratelimit.ErrInvalidKey
	}
	now, err := l.now(ctx)
	if err != nil {
		return false, 0, err
	}

	// Round TTL up so a sub-second period still passes a positive value
	// to EX (Redis EX takes integer seconds).
	ttlSec := int64((l.keyTTL + time.Second - 1) / time.Second)

	res, err := gcraScript.Run(ctx, l.client,
		[]string{l.prefix + key},
		now.UnixNano(),
		int64(l.rate),
		l.burst,
		ttlSec,
	).Result()
	if err != nil {
		return false, 0, fmt.Errorf("ratelimit/redis: script: %w", err)
	}
	pair, ok := res.([]interface{})
	if !ok || len(pair) != 2 {
		return false, 0, errors.New("ratelimit/redis: unexpected script result shape")
	}
	allowed, _ := pair[0].(int64)
	retryNs, _ := pair[1].(int64)
	if allowed == 1 {
		return true, 0, nil
	}
	return false, time.Duration(retryNs), nil
}

func maxDuration(a, b time.Duration) time.Duration {
	if a > b {
		return a
	}
	return b
}
