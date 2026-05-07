# NEW: data/budget — per-tenant token / cost budgets

**Phase**: 5 (Tier-2 infrastructure)
**Module path**: `github.com/bds421/rho-kit/data/budget` (+ memory and redis backends, HTTP middleware, outbound RoundTripper)

## Why

The kit ships rate limiting (per-IP, per-key, GCRA, Redis-backed). What
it lacks — and what every agentic-AI service needs — is **token / cost
budgets per tenant**: a way to bound how many LLM tokens, dollars,
or embeddings a tenant can consume per period without rebuilding the
same logic in every service.

A rate limiter answers "is this caller making too many requests right
now?". A budget answers "has this caller spent more than their cap of
arbitrary expensive units this hour / day / month?". The two are
related but not interchangeable: a budget tracks an aggregate quantity
that the caller chooses (1 request = N tokens), while a limiter tracks
calls.

The integer LLM bill from a single misbehaving tenant blows past five
figures the moment a service ships without this primitive.

## Public API

### `data/budget`

```go
package budget

type Budget interface {
    Consume(ctx context.Context, key string, amount int64) (allowed bool, remaining int64, retryAfter time.Duration, err error)
    Peek(ctx context.Context, key string) (remaining int64, err error)
}

// Optional: backends that support refunds (over-estimate reconciliation).
type Refunder interface {
    Refund(ctx context.Context, key string, amount int64) (remaining int64, err error)
}

// Refund credits `amount` if `b` implements Refunder; ok=false otherwise.
func Refund(ctx context.Context, b Budget, key string, amount int64) (remaining int64, ok bool, err error)
```

Sentinels: `ErrInvalidKey`, `ErrInvalidAmount`.

Documented design choice: **fixed-window resets, not sliding window**.
LLM-cost reporting is naturally fixed-window; operators need clear "X
tokens used so far this hour" semantics. The boundary-doubling trade-off
is explicitly accepted in package docs (worst case 2× cap; for adversarial
rate-limiting use `data/ratelimit/gcra` instead).

### Subpackages

```
data/budget/memory   -- in-process, sync.Map keyed by (key, period); self-compacting on Consume
data/budget/redis    -- atomic Lua INCRBY/DECRBY against per-period bucket key; cross-instance
```

Both implement `Budget` and `Refunder`.

### `httpx/middleware/budget`

```go
func Middleware(b budget.Budget, opts ...Option) func(http.Handler) http.Handler

func WithKeyFunc(fn KeyFunc) Option   // default: tenant.FromContext
func WithAmount(n int64) Option       // default: 1
func WithScope(s string) Option       // X-Budget-Scope header on rejection
```

Returns 429 with `X-Budget-Scope`, `X-Budget-Remaining`, `Retry-After`
on rejection; passes through when KeyFunc returns ok=false (so health
endpoints stay reachable). Backend errors surface as 503 — no silent
fail-open.

### `httpx/budget` (outbound RoundTripper)

```go
func Wrap(rt http.RoundTripper, b budget.Budget, key string, opts ...Option) http.RoundTripper

func WithEstimateHeader(name string) Option   // e.g. X-Estimated-Tokens
func WithActualHeader(name string) Option     // e.g. X-Actual-Tokens; reconcile delta after response
func WithDefaultAmount(n int64) Option
func WithLogger(l Logger) Option

var ErrBudgetExceeded = errors.New(...)
```

Two-phase: pre-charge an estimate, reconcile against the upstream's
reported actual on response (charge under-estimate; refund
over-estimate via the Refunder capability). On rejection returns
`ErrBudgetExceeded` so callers can `errors.Is` to distinguish "we
said no" from "upstream said no".

## Definition of done

- [x] Top-level `Budget` interface + `ErrInvalidKey` / `ErrInvalidAmount`. ✅ `dc525b2`
- [x] Optional `Refunder` capability + `Refund` dispatch helper. ✅ `dc525b2`
- [x] In-memory backend (`data/budget/memory`) with concurrent-admit, period-rollover, and unknown-key Peek tests. ✅ `5bb952e`
- [x] Redis backend (`data/budget/redis`) with atomic Lua, miniredis-driven cross-client test, period-rollover, key-prefix isolation, and `WithRedisTime` option. ✅ `a7beed8`
- [x] HTTP middleware (`httpx/middleware/budget`) with default tenant key, header attachments, and 503-on-backend-error policy. ✅ `07af16f`
- [x] Outbound RoundTripper (`httpx/budget`) with sentinel error, estimate header, and reconciliation against an actual header. ✅ `18745af`
- [x] Audit entry (this file).
- [x] Builder integration ✅ (Wave 2) — `Builder.WithTenantBudget(b, opts...)` activates the inbound budget middleware on the public mux. Default key function reads tenant ID from ctx (composes naturally with `WithMultiTenant`); supply `httpxbudget.WithKeyFunc` for non-tenant scopes. Builder panics on nil store (no silent no-budget). Budget exposed on Infrastructure for handler-side use.
- [ ] Recipe in `docs/ai/http.md` — deferred to docs sweep.

## Design choices worth flagging

- **Period boundary frame**: `floor(unixNs / periodNs)` in UTC. Both backends agree so a service migrating between them sees identical boundaries. The Redis backend defaults to the local clock; `WithRedisTime` shifts to Redis TIME for cross-replica clock-skew-free fairness at the cost of one extra round trip.
- **Fixed window over sliding**: documented in `budget.go` package docs. Maps to vendor invoice lines; bounded backend state; trade-off (2× burst at boundary) accepted.
- **No `Refund` on the base interface**: optional `Refunder` capability + `Refund` helper. Backends that cannot safely refund (e.g. an externally-aggregated counter) opt out by not implementing the interface; the RoundTripper falls back to logging the missed credit, bounded by period length.
- **Backend errors → 503**: the HTTP middleware does NOT silently fail open on Redis unavailability. A misbehaving tenant during a Redis outage is a worse failure mode than refusing service.
- **JSON body for rejection responses**: `{"error":"budget exceeded","code":"BUDGET_EXCEEDED","remaining":N}`. Headers carry the structured info; the body is small companion text.

## Related

- [new/10-data-ratelimit-sliding-window.md](10-data-ratelimit-sliding-window.md) — sibling primitive; rate limit per request vs budget per arbitrary unit.
- [new/20-multitenant-primitives.md](20-multitenant-primitives.md) — `core/tenant` provides the default key for the HTTP middleware.
