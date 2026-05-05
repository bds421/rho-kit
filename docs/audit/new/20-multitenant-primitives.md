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

- [ ] `core/tenant` package with type-distinct `ID` and ctx helpers.
- [ ] `httpx/middleware/tenant` with default-JWT extractor.
- [ ] Cache + idempotency wrappers.
- [ ] Per-tenant rate-limit middleware.
- [ ] `promutil/labelguard` for cardinality protection.
- [ ] Builder `WithMultiTenant`.
- [ ] Tests covering each integration's tenant isolation.
- [ ] Recipe in `docs/ai/utilities.md`.
