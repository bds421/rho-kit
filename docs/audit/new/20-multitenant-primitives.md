# NEW: core/tenant + tenant-aware integrations

**Phase**: 5 (Tier‑2; large area, ship incrementally)
**Module path**: `github.com/bds421/rho-kit/core/tenant` (core type) + integrations across existing packages.

## Why

The kit has no multi-tenant primitives. Every SaaS-style consumer rolls their own tenant context, tenant-scoped Redis keys, tenant-aware rate limits, and tenant-aware metric labels. Several of this audit's high-cardinality and key-isolation findings (Redis queue shared `:processing`, idempotency cross-user replay, metric label explosion) all relate.

This package sets a project-wide convention; downstream integrations build on it.

## Public API

### `core/tenant`

```go
package tenant

// ID identifies a tenant. Type-distinct from string to make accidental
// usage in non-tenant contexts a compile error.
type ID string

// FromContext returns the tenant ID set by middleware/interceptor, or empty
// if none. ContextKey is unexported; consumers must go through these helpers.
func FromContext(ctx context.Context) (ID, bool)

// WithID returns a child context carrying the tenant ID.
func WithID(ctx context.Context, id ID) context.Context

// Required returns the tenant ID or an error if absent — for handlers that
// must run in a tenant scope.
func Required(ctx context.Context) (ID, error)
```

### `httpx/middleware/tenant`

```go
// Middleware extracts the tenant ID from the request (default: JWT
// "tenant_id" claim, configurable extractor) and stores it in context.
//
// Returns 400 if the request must have a tenant but doesn't.
func Middleware(opts ...Option) func(http.Handler) http.Handler

func WithExtractor(fn func(*http.Request) (tenant.ID, bool)) Option
func WithRequired(bool) Option // default true
```

### `data/cache/tenant` (key builder)

```go
// Key returns "tenant:<id>:<key>" — never collides across tenants.
// Panics if ctx has no tenant ID and required=true.
func Key(ctx context.Context, key string) string

// Wrap wraps a Cache so all Get/Set/Delete operations are tenant-scoped
// using the tenant ID from ctx.
func Wrap(c cache.Cache) cache.Cache
```

### `data/idempotency/tenant`

Wrap idempotency `Store` so the fingerprint includes tenant ID. Closes the cross-tenant cache-collision risk in `idempotency.Middleware` even if `WithUserExtractor` isn't enough (e.g., service-to-service calls where user is the tenant).

### `httpx/middleware/ratelimit/tenant`

Per-tenant rate limit keyed off `tenant.FromContext`. Combine with IP limit for layered defense.

### `observability/promutil/labelguard`

```go
// AllowedLabels enforces a label allowlist on a CounterVec/HistogramVec at
// observation time. Reject (with a counter increment) labels outside the
// allowlist to prevent cardinality explosion when accidentally including
// raw user IDs.
type AllowedLabels struct{ /* ... */ }

func NewAllowedLabels(allowed map[string][]string) *AllowedLabels
func (g *AllowedLabels) Observe(vec *prometheus.HistogramVec, labels prometheus.Labels, val float64)
```

### Optional: `data/sqldb/rls`

Helpers for PostgreSQL row-level-security policies based on tenant ID. Two patterns:
- Set `app.tenant_id` session variable in a `BEFORE` hook so RLS policies fire.
- Helpers to construct GORM query scopes that enforce a `tenant_id = ?` predicate.

## Builder integration

```go
b.WithMultiTenant(extractor func(*http.Request) (tenant.ID, bool))
```

Activates the tenant middleware on the public mux, wraps the default cache and idempotency store with tenant scoping.

## Definition of done

- [x] `core/tenant` package with type-distinct `ID`, ctx helpers, `Required` (ErrMissing). ✅ this PR
- [x] `httpx/middleware/tenant` with default header extractor + custom-extractor option (JWT extraction stays in caller code so httpx doesn't pull a JWT dep). ✅ this PR
- [x] Cache + idempotency wrappers — `data/cache/tenant` (aea8d61), `data/idempotency/tenant` (370441f). Both prefix the storage key with `tenant:<id>:` so the same raw key under two tenants resolves to disjoint slots. Idempotency design choice: namespace the *key* (not the body fingerprint) so backend-layer isolation holds even if a backend bug ignores fingerprint, and so a fresh request from tenant B that happens to share tenant A's cached body does not falsely report 422.
- [x] Per-tenant rate-limit middleware — `httpx/middleware/ratelimit/tenant` (5ea90d6). Composes on top of the IP middleware (both must pass); 429 responses set `X-RateLimit-Scope: tenant` so on-call can disambiguate the firing budget. Missing tenant returns 400.
- [x] `promutil/labelguard` for cardinality protection — `observability/promutil/labelguard` (8c46c50). `AllowedLabels` wrapping CounterVec/HistogramVec; rejected observations silently drop (user-input-derived labels must not panic) and increment `labelguard_rejected_total{vec, label}`.
- [x] Builder `WithMultiTenant(extractor, required)` ✅ (Wave 2) — installs the tenant middleware on the public mux. Composition order: signedrequest (outermost) → tenant → budget → handlers, so unsigned requests reject before tenant work and tenant ID lands on ctx before budget reads it.
- [x] Tests: zero-ID never appears as present; nil ctx tolerated; Required surfaces ErrMissing; middleware rejects 400 on missing tenant for state-changing methods; safe methods pass through; custom extractor honoured.
- [ ] Recipe in `docs/ai/utilities.md` (deferred to docs sweep).
