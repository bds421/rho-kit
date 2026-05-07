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

// scopedBulk extends scoped with the BulkCache fast paths. It is
// returned from Wrap when the inner cache implements BulkCache so the
// wrapper does not silently downgrade MGet/MSet/SetNX to the racy
// fallbacks in [cache.SetNX] and friends.
type scopedBulk struct {
	scoped
	bulk cache.BulkCache
}

// Wrap returns a [cache.Cache] that prefixes every key with the
// caller's tenant ID. The returned cache panics on any operation
// invoked without a tenant ID on ctx — see package doc for rationale.
//
// When inner implements [cache.BulkCache], the returned value also
// implements BulkCache so MGet/MSet/SetNX are forwarded with tenant
// scoping rather than falling back to the per-key Cache helpers.
//
// Wrap panics on a nil inner cache; callers should treat a nil inner
// as a programming error and the wrapper makes that explicit upfront.
func Wrap(inner cache.Cache) cache.Cache {
	if inner == nil {
		panic("cache/tenant: inner cache must not be nil")
	}
	if bulk, ok := inner.(cache.BulkCache); ok {
		return &scopedBulk{scoped: scoped{inner: inner}, bulk: bulk}
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

// MGet rewrites every key and forwards a single bulk read to the inner
// BulkCache. The result map is rebuilt with the caller's raw keys so
// scoping is invisible to consumers.
func (s *scopedBulk) MGet(ctx context.Context, keys []string) (map[string][]byte, error) {
	if len(keys) == 0 {
		return map[string][]byte{}, nil
	}
	scoped := make([]string, len(keys))
	rawByScoped := make(map[string]string, len(keys))
	for i, k := range keys {
		sk := scopedKey(ctx, k)
		scoped[i] = sk
		rawByScoped[sk] = k
	}
	got, err := s.bulk.MGet(ctx, scoped)
	if err != nil {
		return nil, err
	}
	out := make(map[string][]byte, len(got))
	for sk, v := range got {
		raw, ok := rawByScoped[sk]
		if !ok {
			continue
		}
		out[raw] = v
	}
	return out, nil
}

// MSet rewrites every key and forwards a single bulk write to the inner
// BulkCache, preserving whatever atomicity guarantees that backend
// provides (Redis pipeline, in-memory single-lock fan-out).
func (s *scopedBulk) MSet(ctx context.Context, items map[string][]byte, ttl time.Duration) error {
	if len(items) == 0 {
		return nil
	}
	scoped := make(map[string][]byte, len(items))
	for k, v := range items {
		scoped[scopedKey(ctx, k)] = v
	}
	return s.bulk.MSet(ctx, scoped, ttl)
}

// SetNX rewrites the key and forwards to the inner BulkCache so that
// cross-process compute-once semantics survive tenant scoping. Without
// this, the [cache.SetNX] free function falls back to a racy
// Exists+Set sequence.
func (s *scopedBulk) SetNX(ctx context.Context, key string, value []byte, ttl time.Duration) (bool, error) {
	return s.bulk.SetNX(ctx, scopedKey(ctx, key), value, ttl)
}
