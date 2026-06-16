// Package redis implements a Redis-backed GCRA [ratelimit.Limiter].
//
// State per key is a single int64 microsecond timestamp ("theoretical
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
	"time"
	"unicode"
	"unicode/utf8"

	goredis "github.com/redis/go-redis/v9"

	"github.com/bds421/rho-kit/core/v2/clock"
	"github.com/bds421/rho-kit/core/v2/redact"
	"github.com/bds421/rho-kit/data/v2/ratelimit"
)

// gcraScriptSrc is the atomic GCRA evaluation source.
//
//	KEYS[1] = per-key state ("<prefix>:<key>")
//	ARGV[1] = now in microseconds (caller's clock)
//	ARGV[2] = rate (period / burst) in microseconds, rounded up
//	ARGV[3] = burst (cells)
//	ARGV[4] = key TTL in seconds (>=1)
//
// Returns: {allowed (0|1), retryAfter microseconds when denied}.
//
// The TAT is written with string.format("%.0f", ...) rather than as a raw Lua
// number. Redis serialises a Lua number argument with %.14g; current
// Unix-microsecond timestamps have 16 significant digits, so a raw number would
// be rounded to roughly a 100µs grid and sub-100µs rate increments would be
// lost. Formatting to a decimal string in Lua passes the exact integer to SET.
const gcraScriptSrc = `
local now    = tonumber(ARGV[1])
local rate   = tonumber(ARGV[2])
local burst  = tonumber(ARGV[3])
local ttl    = tonumber(ARGV[4])

local tat = tonumber(redis.call("GET", KEYS[1]))
if not tat or tat < now then
  tat = now
end
local allowAt = tat - (burst - 1) * rate
if now < allowAt then
  return {0, allowAt - now + 1}
end
local newTat = tat + rate
redis.call("SET", KEYS[1], string.format("%.0f", newTat), "EX", ttl)
return {1, 0}
`

// gcraScript is the atomic GCRA evaluation.
var gcraScript = goredis.NewScript(gcraScriptSrc)

// Limiter is a per-key Redis-backed GCRA [ratelimit.Limiter].
// Safe for concurrent use — all per-call state lives in Redis; the
// embedded goredis client is itself goroutine-safe.
type Limiter struct {
	client goredis.UniversalClient
	prefix string
	period time.Duration
	burst  int
	rate   time.Duration
	rateUS int64
	keyTTL time.Duration
	now    func(ctx context.Context) (time.Time, error)
}

// Option configures a Limiter.
type Option func(*Limiter)

// WithKeyPrefix sets the prefix prepended to every Redis key. Default:
// "ratelimit:gcra:". Pick a unique prefix per logical limiter so two
// limiters with different (period, burst) configurations on the same
// Redis can't collide.
//
// Audit FR-058: panics on empty, invalid, or >maxKeyPrefixLen prefix so a
// misconfigured prefix cannot inflate or corrupt every Redis key.
func WithKeyPrefix(p string) Option {
	if p == "" {
		panic("ratelimit/redis: WithKeyPrefix requires a non-empty prefix")
	}
	if len(p) > maxKeyPrefixLen {
		panic("ratelimit/redis: WithKeyPrefix prefix exceeds maximum length")
	}
	if containsInvalidStringBytes(p) {
		panic("ratelimit/redis: WithKeyPrefix prefix contains invalid characters")
	}
	return func(l *Limiter) { l.prefix = p }
}

// maxKeyPrefixLen caps Redis key prefixes (audit FR-058) to prevent
// pathological key sizes. Raw limiter keys use [ratelimit.ValidateKey].
const maxKeyPrefixLen = 128

// WithKeyTTL overrides the per-key expiration. Default: max(period,
// 60s). Bounding TTL keeps cold keys from accumulating in Redis.
//
// The TTL must be at least `period` — shorter and Redis can evict the
// state mid-window, defeating the cross-replica budget for that key.
func WithKeyTTL(d time.Duration) Option {
	if d <= 0 {
		panic("ratelimit/redis: WithKeyTTL requires a positive duration")
	}
	return func(l *Limiter) { l.keyTTL = d }
}

// WithClock overrides the local clock used to compute `now`. Tests
// only.
func WithClock(now clock.Func) Option {
	if now == nil {
		panic("ratelimit/redis: WithClock clock must not be nil")
	}
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
				return time.Time{}, redact.WrapError("ratelimit/redis: TIME", err)
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
//
// Redis Lua arithmetic cannot represent current Unix nanosecond timestamps
// exactly, so this backend stores TAT in microseconds. Sub-microsecond rates
// are rounded up to 1 microsecond, which is conservative for enforcement and
// still below Redis/network scheduling resolution.
func New(client goredis.UniversalClient, period time.Duration, burst int, opts ...Option) *Limiter {
	if client == nil {
		panic("ratelimit/redis: New client must not be nil")
	}
	if period <= 0 {
		panic("ratelimit/redis: New period must be > 0")
	}
	if burst < 1 {
		panic("ratelimit/redis: New burst must be >= 1")
	}
	rate := period / time.Duration(burst)
	if rate <= 0 {
		panic("ratelimit/redis: New period/burst rounds to zero (burst exceeds period in nanoseconds); pick a longer period or smaller burst")
	}
	l := &Limiter{
		client: client,
		prefix: "ratelimit:gcra:",
		period: period,
		burst:  burst,
		rate:   rate,
		rateUS: ceilDurationMicros(rate),
		keyTTL: maxDuration(period, time.Minute),
		now: func(_ context.Context) (time.Time, error) {
			return time.Now(), nil
		},
	}
	for _, o := range opts {
		if o == nil {
			panic("ratelimit/redis: New option must not be nil")
		}
		o(l)
	}
	if l.keyTTL < period {
		panic("ratelimit/redis: New key TTL must be >= period (otherwise Redis can evict mid-window)")
	}
	return l
}

func (l *Limiter) ready() error {
	if l == nil ||
		l.client == nil ||
		l.prefix == "" ||
		len(l.prefix) > maxKeyPrefixLen ||
		containsInvalidStringBytes(l.prefix) ||
		l.period <= 0 ||
		l.burst < 1 ||
		l.rate <= 0 ||
		l.rateUS <= 0 ||
		l.keyTTL < l.period ||
		l.now == nil {
		return ratelimit.ErrInvalidLimiter
	}
	return nil
}

// Allow reports whether key's next event is permitted. retryAfter is
// the time-until-next-allowed when denied.
func (l *Limiter) Allow(ctx context.Context, key string) (bool, time.Duration, error) {
	if err := l.ready(); err != nil {
		return false, 0, err
	}
	if err := ratelimit.ValidateKey(key); err != nil {
		return false, 0, err
	}
	now, err := l.now(ctx)
	if err != nil {
		return false, 0, err
	}

	// Round TTL up so a sub-second period still passes a positive value
	// to EX (Redis EX takes integer seconds).
	ttlSec := ceilDurationSeconds(l.keyTTL)

	res, err := gcraScript.Run(ctx, l.client,
		[]string{l.prefix + key},
		now.UnixMicro(),
		l.rateUS,
		l.burst,
		ttlSec,
	).Result()
	if err != nil {
		return false, 0, redact.WrapError("ratelimit/redis: script", err)
	}
	allowed, retryUS, err := parseScriptResult(res)
	if err != nil {
		return false, 0, err
	}
	if allowed {
		return true, 0, nil
	}
	return false, durationFromMicros(retryUS), nil
}

// parseScriptResult decodes the GCRA Lua script's reply, which is expected to
// be a 2-element array of int64 {allowed (0|1), retryAfter microseconds}. Any
// other shape — wrong length OR non-int64 members — is reported as an explicit
// error rather than coerced into a silent deny, so a malformed reply is never
// indistinguishable from a legitimate rate-limit rejection.
func parseScriptResult(res any) (allowed bool, retryUS int64, err error) {
	pair, ok := res.([]any)
	if !ok || len(pair) != 2 {
		return false, 0, errors.New("ratelimit/redis: unexpected script result shape")
	}
	allowedRaw, ok := pair[0].(int64)
	if !ok {
		return false, 0, errors.New("ratelimit/redis: unexpected script result shape")
	}
	retryUS, ok = pair[1].(int64)
	if !ok {
		return false, 0, errors.New("ratelimit/redis: unexpected script result shape")
	}
	return allowedRaw == 1, retryUS, nil
}

const maxDurationValue = time.Duration(1<<63 - 1)

func ceilDurationMicros(d time.Duration) int64 {
	micros := d / time.Microsecond
	if d%time.Microsecond != 0 {
		micros++
	}
	return int64(micros)
}

func ceilDurationSeconds(d time.Duration) int64 {
	seconds := d / time.Second
	if d%time.Second != 0 {
		seconds++
	}
	if seconds < 1 {
		return 1
	}
	return int64(seconds)
}

func durationFromMicros(micros int64) time.Duration {
	if micros <= 0 {
		return time.Nanosecond
	}
	maxMicros := int64(maxDurationValue / time.Microsecond)
	if micros > maxMicros {
		return maxDurationValue
	}
	return time.Duration(micros) * time.Microsecond
}

func maxDuration(a, b time.Duration) time.Duration {
	if a > b {
		return a
	}
	return b
}

func containsInvalidStringBytes(s string) bool {
	if !utf8.ValidString(s) {
		return true
	}
	for _, r := range s {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return true
		}
	}
	return false
}
