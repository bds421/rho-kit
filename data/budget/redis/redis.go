// Package redis implements a Redis-backed [budget.Budget] suitable
// for sharing fixed-window cost budgets across many app replicas.
//
// State per (key, period bucket) is a single integer counter:
// `<prefix>:<key>:<period-id>`. Every Consume call is one atomic
// Lua script — read counter, optimistically INCRBY, on overflow
// DECRBY and reject — so multi-replica races never overspend the
// cap. The TTL on each bucket key is `period + grace` so stale
// buckets evict on their own without a sweep job.
//
// Use this when:
//
//   - The same per-tenant budget must apply across multiple replicas
//     (per-tenant LLM-token quota, per-customer dollar cap).
//   - You can tolerate one Redis round trip per accounted call.
//
// Compared to data/budget/memory the trade-offs are:
//
//   - Latency: ~0.5–2 ms per call; in-memory is ns.
//   - Availability: a Redis outage stalls every accounted request
//     unless the caller wraps the budget in a fail-open/fail-closed
//     policy. The package does NOT silently fail open — it surfaces
//     the Redis error.
//
// # Time source
//
// Consume takes the local time as the period reference by default.
// For strict cross-replica fairness in the presence of clock skew,
// set [WithRedisTime] to use Redis's TIME command (one extra round
// trip).
package redis

import (
	"context"
	"errors"
	"fmt"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/bds421/rho-kit/data/budget"
)

// budgetScript is the atomic Consume evaluation.
//
//	KEYS[1] = bucket counter ("<prefix>:<key>:<period-id>")
//	ARGV[1] = amount to charge (>=0)
//	ARGV[2] = cap
//	ARGV[3] = key TTL in seconds (>=1)
//	ARGV[4] = retry-after milliseconds (time until next period)
//
// Returns: {allowed (0|1), remaining int}.
//
// The optimistic INCRBY then conditional DECRBY pattern is the
// canonical "atomic test-and-set against a cap" idiom in Redis Lua:
// it tolerates dozens of concurrent script invocations against the
// same key with no extra synchronisation in the application.
var budgetScript = goredis.NewScript(`
local amount = tonumber(ARGV[1])
local cap    = tonumber(ARGV[2])
local ttl    = tonumber(ARGV[3])

if amount == 0 then
  local cur = tonumber(redis.call("GET", KEYS[1])) or 0
  local rem = cap - cur
  if rem < 0 then rem = 0 end
  return {1, rem}
end

local newUsed = redis.call("INCRBY", KEYS[1], amount)
if newUsed > cap then
  redis.call("DECRBY", KEYS[1], amount)
  local cur = newUsed - amount
  local rem = cap - cur
  if rem < 0 then rem = 0 end
  return {0, rem}
end
redis.call("EXPIRE", KEYS[1], ttl)
return {1, cap - newUsed}
`)

// peekScript is a non-charging read that returns the current
// remaining for the active bucket.
var peekScript = goredis.NewScript(`
local cap = tonumber(ARGV[1])
local cur = tonumber(redis.call("GET", KEYS[1])) or 0
local rem = cap - cur
if rem < 0 then rem = 0 end
return rem
`)

// refundScript credits `amount` back to the bucket counter. The
// floor at zero protects against an over-credit (caller refunding
// more than was charged) inflating future Consume calls past the
// cap — refunds should never grant more headroom than the cap.
var refundScript = goredis.NewScript(`
local amount = tonumber(ARGV[1])
local cap    = tonumber(ARGV[2])

if amount == 0 then
  local cur = tonumber(redis.call("GET", KEYS[1])) or 0
  local rem = cap - cur
  if rem < 0 then rem = 0 end
  return rem
end

local cur = tonumber(redis.call("GET", KEYS[1])) or 0
local newUsed = cur - amount
if newUsed < 0 then newUsed = 0 end
if newUsed == 0 then
  redis.call("DEL", KEYS[1])
else
  redis.call("SET", KEYS[1], newUsed, "KEEPTTL")
end
return cap - newUsed
`)

// Budget is a per-key Redis-backed [budget.Budget].
type Budget struct {
	client goredis.UniversalClient
	prefix string
	cap    int64
	period time.Duration
	keyTTL time.Duration
	now    func(ctx context.Context) (time.Time, error)
}

// Option configures a [Budget].
type Option func(*Budget)

// WithKeyPrefix sets the prefix prepended to every Redis key.
// Default: "budget:". Pick a unique prefix per logical budget so two
// budgets with different (cap, period) configurations on the same
// Redis can't collide.
func WithKeyPrefix(p string) Option {
	return func(b *Budget) { b.prefix = p }
}

// WithKeyTTL overrides the per-bucket expiration. Default:
// `period + 1m` to keep cold buckets from accumulating in Redis.
//
// The TTL must be at least `period` — shorter and Redis can evict
// the state mid-window, defeating the cross-replica budget for that
// key.
func WithKeyTTL(d time.Duration) Option {
	return func(b *Budget) { b.keyTTL = d }
}

// WithClock overrides the local clock used to compute the current
// period (tests only).
func WithClock(now func() time.Time) Option {
	return func(b *Budget) {
		b.now = func(_ context.Context) (time.Time, error) { return now(), nil }
	}
}

// WithRedisTime uses the Redis server's clock for the current period
// reference. Costs one extra round trip per Consume but eliminates
// cross-replica clock-skew artefacts. Use when the budget must be
// strictly enforced (per-tenant LLM-cost cap with billing
// implications).
func WithRedisTime() Option {
	return func(b *Budget) {
		b.now = func(ctx context.Context) (time.Time, error) {
			t, err := b.client.Time(ctx).Result()
			if err != nil {
				return time.Time{}, fmt.Errorf("budget/redis: TIME: %w", err)
			}
			return t, nil
		}
	}
}

// New constructs a Budget allowing up to `cap` units per `period`,
// persisted in `client`.
//
// Panics on misconfiguration (nil client, zero cap, zero period,
// TTL < period).
func New(client goredis.UniversalClient, cap int64, period time.Duration, opts ...Option) *Budget {
	if client == nil {
		panic("budget/redis: client must not be nil")
	}
	if cap <= 0 {
		panic("budget/redis: cap must be > 0")
	}
	if period <= 0 {
		panic("budget/redis: period must be > 0")
	}
	b := &Budget{
		client: client,
		prefix: "budget:",
		cap:    cap,
		period: period,
		keyTTL: period + time.Minute,
		now: func(_ context.Context) (time.Time, error) {
			return time.Now(), nil
		},
	}
	for _, o := range opts {
		o(b)
	}
	if b.keyTTL < period {
		panic("budget/redis: key TTL must be >= period (otherwise Redis can evict mid-window)")
	}
	return b
}

// periodOf returns the integer period id and the wall-clock instant
// the next window begins, mirroring the in-memory backend's frame
// so a service moving between backends sees identical boundaries.
func (b *Budget) periodOf(t time.Time) (int64, time.Time) {
	periodNs := int64(b.period)
	id := t.UTC().UnixNano() / periodNs
	nextStart := time.Unix(0, (id+1)*periodNs).UTC()
	return id, nextStart
}

func (b *Budget) bucketKey(key string, periodID int64) string {
	return fmt.Sprintf("%s%s:%d", b.prefix, key, periodID)
}

// ttlSeconds rounds the configured TTL up to whole seconds so a
// sub-second period still passes a positive integer to EX.
func (b *Budget) ttlSeconds() int64 {
	return int64((b.keyTTL + time.Second - 1) / time.Second)
}

// Consume implements [budget.Budget].
func (b *Budget) Consume(ctx context.Context, key string, amount int64) (bool, int64, time.Duration, error) {
	if key == "" {
		return false, 0, 0, budget.ErrInvalidKey
	}
	if amount < 0 {
		return false, 0, 0, budget.ErrInvalidAmount
	}
	now, err := b.now(ctx)
	if err != nil {
		return false, 0, 0, err
	}
	periodID, nextStart := b.periodOf(now)

	res, err := budgetScript.Run(ctx, b.client,
		[]string{b.bucketKey(key, periodID)},
		amount,
		b.cap,
		b.ttlSeconds(),
	).Result()
	if err != nil {
		return false, 0, 0, fmt.Errorf("budget/redis: script: %w", err)
	}
	pair, ok := res.([]interface{})
	if !ok || len(pair) != 2 {
		return false, 0, 0, errors.New("budget/redis: unexpected script result shape")
	}
	allowed, _ := pair[0].(int64)
	remaining, _ := pair[1].(int64)

	if allowed == 1 {
		return true, remaining, 0, nil
	}
	return false, remaining, time.Until(nextStart), nil
}

// Refund implements [budget.Refunder]. Refunding past the cap
// clamps at the cap (`used` floors at zero) so refunds never
// inflate the budget above its configured limit.
func (b *Budget) Refund(ctx context.Context, key string, amount int64) (int64, error) {
	if key == "" {
		return 0, budget.ErrInvalidKey
	}
	if amount < 0 {
		return 0, budget.ErrInvalidAmount
	}
	now, err := b.now(ctx)
	if err != nil {
		return 0, err
	}
	periodID, _ := b.periodOf(now)

	res, err := refundScript.Run(ctx, b.client,
		[]string{b.bucketKey(key, periodID)},
		amount,
		b.cap,
	).Result()
	if err != nil {
		return 0, fmt.Errorf("budget/redis: script: %w", err)
	}
	rem, _ := res.(int64)
	return rem, nil
}

// Peek implements [budget.Budget].
func (b *Budget) Peek(ctx context.Context, key string) (int64, error) {
	if key == "" {
		return 0, budget.ErrInvalidKey
	}
	now, err := b.now(ctx)
	if err != nil {
		return 0, err
	}
	periodID, _ := b.periodOf(now)

	res, err := peekScript.Run(ctx, b.client,
		[]string{b.bucketKey(key, periodID)},
		b.cap,
	).Result()
	if err != nil {
		return 0, fmt.Errorf("budget/redis: script: %w", err)
	}
	rem, _ := res.(int64)
	return rem, nil
}
