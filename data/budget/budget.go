// Package budget defines the algorithm-agnostic [Budget] interface
// used across the kit's per-tenant cost-budget primitives.
//
// A Budget tracks consumption of an arbitrary numeric quantity —
// LLM tokens, dollars × 1000, embedding count, "units of work" —
// against a per-key cap that resets at fixed-window boundaries
// (top-of-hour, top-of-day). Use it to bound how much a tenant can
// consume per period without rebuilding the same logic in every
// service.
//
// Implementations live in subpackages so consumers only depend on
// what they need:
//
//   - data/budget/memory — in-process, single-instance.
//   - data/budget/redis — cross-instance via atomic Lua.
//
// All implementations satisfy [Budget] so HTTP middleware, gRPC
// interceptors, and outbound RoundTrippers can swap backends without
// rewiring the call sites.
//
// # Why fixed windows, not sliding
//
// Budgets reset at fixed-window boundaries (e.g. every hour boundary
// at :00) — NOT a sliding window. This is a deliberate trade-off:
//
//   - LLM-cost reporting from upstream providers is naturally
//     fixed-window. "X tokens used this hour" maps directly onto a
//     vendor invoice line; a sliding window does not.
//   - Operators need clear "budget remaining for the current period"
//     semantics they can show in dashboards and alert on. With a
//     fixed window, "remaining" is a single integer; with a sliding
//     window it depends on the shape of past consumption.
//   - Charging a single boundary refresh per period bounds the
//     state needed in the backend (one counter + TTL per key).
//
// The downside is the well-known boundary-doubling effect: a tenant
// can spend their full cap at 9:59 and again at 10:00. For
// per-tenant LLM-cost limits this is acceptable — the worst case is
// 2× the per-period cap, which is still bounded; for adversarial
// rate-limiting use a smoothing limiter (data/ratelimit/gcra)
// instead.
//
// # Period boundaries
//
// Periods are floor(now / period) × period in the UTC reference
// frame. Backends document whether they use the local clock or a
// shared clock (e.g. Redis TIME) for the current time. With a local
// clock, two replicas with skewed clocks may briefly disagree about
// which period a charge belongs to. With a shared clock the
// boundary is exact at the cost of one extra round trip.
package budget

import (
	"context"
	"errors"
	"reflect"
	"time"
	"unicode"
	"unicode/utf8"
)

// MaxKeyLen caps budget keys across all backends. Redis can accept much
// larger keys, but long attacker-controlled tenant/user keys inflate memory,
// logs, and network traffic. Keep the portable budget contract bounded.
const MaxKeyLen = 256

// ErrInvalidKey is returned when key is empty, oversized, invalid UTF-8,
// or contains whitespace/control characters. (An empty key collapses every
// caller into a single bucket — almost certainly a bug rather than the intent.)
var ErrInvalidKey = errors.New("budget: key is invalid")

// ErrInvalidAmount is returned when amount is negative. Zero is
// allowed (a no-op consume / "is anything left?" probe).
var ErrInvalidAmount = errors.New("budget: amount must not be negative")

// ErrInvalidBudget is returned when a Budget method/helper is invoked
// with a nil or otherwise uninitialized budget implementation.
var ErrInvalidBudget = errors.New("budget: budget is not initialized")

// Budget tracks consumption of an arbitrary numeric quantity
// against a per-key cap for the current period.
type Budget interface {
	// Consume attempts to charge `amount` against `key`'s remaining
	// budget for the current period. Returns:
	//
	//   - allowed=true: the charge fit; remaining is updated.
	//   - allowed=false: the charge would exceed the cap; remaining
	//     is unchanged. retryAfter hints when the next period starts.
	//   - err: backend or argument error. allowed must be false.
	//
	// amount==0 is a no-op: it returns the current remaining without
	// charging. Use [Peek] for a clearer call site.
	Consume(ctx context.Context, key string, amount int64) (allowed bool, remaining int64, retryAfter time.Duration, err error)

	// Peek returns current remaining budget without charging. Useful
	// for advisory headers (X-Budget-Remaining) and dashboards.
	Peek(ctx context.Context, key string) (remaining int64, err error)
}

// ValidateKey enforces the portable budget key contract shared by all
// backends. Keys are arbitrary UTF-8 identifiers (tenant IDs, user IDs,
// route-scoped budget names) but must be non-empty, bounded, and not contain
// whitespace/control characters.
func ValidateKey(key string) error {
	if key == "" ||
		len(key) > MaxKeyLen ||
		!utf8.ValidString(key) ||
		containsInvalidKeyRune(key) {
		return ErrInvalidKey
	}
	return nil
}

func containsInvalidKeyRune(s string) bool {
	for _, r := range s {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return true
		}
	}
	return false
}

// Refunder is implemented by [Budget] backends that support
// crediting a previously-consumed amount back to the current
// period. Use it via [Refund] which falls back gracefully when the
// backend does not implement it.
//
// Two-phase patterns (estimate-then-reconcile, e.g. the outbound
// RoundTripper in httpx/budget) need refunds; backends that cannot
// safely refund (e.g. an externally-aggregated counter) can opt out
// by simply not implementing this interface.
type Refunder interface {
	// Refund credits `amount` back to `key`'s current period. amount
	// must be >= 0 (a zero refund is a no-op). The returned remaining
	// reflects the refunded value but is capped at the configured
	// per-period cap — refunds never inflate the budget past its
	// limit.
	Refund(ctx context.Context, key string, amount int64) (remaining int64, err error)
}

// Refund credits `amount` back to `key` if `b` implements [Refunder].
// Returns ok=false when the backend has no refund capability so the
// caller can decide whether to log, ignore, or wait for the next
// period rollover. amount must be >= 0.
//
// Argument validation runs at this layer so callers see consistent
// errors regardless of optional backend capability — a bad refund
// must not look like a harmless unsupported refund.
func Refund(ctx context.Context, b Budget, key string, amount int64) (remaining int64, ok bool, err error) {
	if err := ValidateKey(key); err != nil {
		return 0, false, err
	}
	if amount < 0 {
		return 0, false, ErrInvalidAmount
	}
	if isNilBudget(b) {
		return 0, false, ErrInvalidBudget
	}
	if r, isRefunder := b.(Refunder); isRefunder {
		rem, rerr := r.Refund(ctx, key, amount)
		return rem, true, rerr
	}
	return 0, false, nil
}

func isNilBudget(b Budget) bool {
	if b == nil {
		return true
	}
	v := reflect.ValueOf(b)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return v.IsNil()
	default:
		return false
	}
}
