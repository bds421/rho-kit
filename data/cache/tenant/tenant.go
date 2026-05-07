// Package tenant provides a tenant-scoped wrapper around any
// [cache.Cache]. Every key passed to the wrapper is rewritten as
// "tenant:<id>:<key>" before reaching the underlying backend, ensuring
// two tenants that pick identical raw keys still occupy disjoint
// slots.
//
// Why a wrapper rather than asking callers to prefix their own keys?
//
//   - Forgetting the prefix is a silent data-leak (tenant A reads
//     tenant B's cached entry). Centralising the prefix removes the
//     class of bug from review surface.
//   - Existing call sites that are not yet tenant-aware can be
//     migrated by swapping the cache instance — no per-call edits.
//
// The wrapper requires a tenant ID on the request context. Absence is
// a programming error (the wrapper only makes sense in tenant-scoped
// code paths) and produces a panic from [tenant.Required] — callers
// that may legitimately run outside a tenant scope should keep using
// the bare cache.
package tenant

import (
	"context"
	"strconv"
	"time"

	coretenant "github.com/bds421/rho-kit/core/tenant"
	"github.com/bds421/rho-kit/data/cache"
)

// keyPrefix is the namespace placed in front of every wrapped key. It
// is intentionally short to keep the per-entry overhead small while
// still being grep-able in a Redis MONITOR trace.
const keyPrefix = "tenant:"

// scoped is the concrete wrapper. It holds the underlying cache and
// rewrites every key on the way in. Reads that hit ErrCacheMiss bubble
// up unchanged so callers can branch on errors.Is as usual.
type scoped struct {
	inner cache.Cache
}

// Wrap returns a [cache.Cache] that prefixes every key with the
// caller's tenant ID. The returned cache panics on any operation
// invoked without a tenant ID on ctx — see package doc for rationale.
//
// Wrap panics on a nil inner cache; callers should treat a nil inner
// as a programming error and the wrapper makes that explicit upfront.
func Wrap(inner cache.Cache) cache.Cache {
	if inner == nil {
		panic("cache/tenant: inner cache must not be nil")
	}
	return &scoped{inner: inner}
}

// scopedKey rewrites raw to "tenant:<len(id)>:<id>:<raw>". Panics if
// ctx carries no tenant ID. The empty-key case is left to the inner
// backend's own validation so we don't double-validate or skew error
// semantics.
//
// The length prefix is the load-bearing element here. Without it,
// tenant `"a:b"` with key `"c"` would produce the same scoped key as
// tenant `"a"` with key `"b:c"` — a silent cross-tenant leak. Encoding
// the tenant length first makes the parse unambiguous regardless of
// which bytes appear in the tenant ID.
//
// Defence-in-depth: [coretenant.NewID] also rejects ':' in tenant IDs,
// but the length prefix means we stay safe even if a caller routes a
// malformed ID through [coretenant.NewIDUnchecked] or constructs
// `coretenant.ID(s)` directly.
func scopedKey(ctx context.Context, raw string) string {
	id, err := coretenant.Required(ctx)
	if err != nil {
		panic("cache/tenant: " + err.Error())
	}
	s := string(id)
	return keyPrefix + strconv.Itoa(len(s)) + ":" + s + ":" + raw
}

// Get rewrites the key and delegates.
func (s *scoped) Get(ctx context.Context, key string) ([]byte, error) {
	return s.inner.Get(ctx, scopedKey(ctx, key))
}

// Set rewrites the key and delegates. ttl semantics are unchanged.
func (s *scoped) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	return s.inner.Set(ctx, scopedKey(ctx, key), value, ttl)
}

// Delete rewrites the key and delegates.
func (s *scoped) Delete(ctx context.Context, key string) error {
	return s.inner.Delete(ctx, scopedKey(ctx, key))
}

// Exists rewrites the key and delegates.
func (s *scoped) Exists(ctx context.Context, key string) (bool, error) {
	return s.inner.Exists(ctx, scopedKey(ctx, key))
}
