// Package tenant provides a tenant-scoped wrapper around any
// [cache.Cache]. Every key passed to the wrapper is rewritten with
// [coretenant.Key] before reaching the underlying backend, ensuring
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
// reported as an error from cache operations — callers that may
// legitimately run outside a tenant scope should keep using the bare
// cache.
package tenant

import (
	"context"
	"time"

	"github.com/bds421/rho-kit/core/v2/redact"
	coretenant "github.com/bds421/rho-kit/core/v2/tenant"
	"github.com/bds421/rho-kit/data/v2/cache"
)

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
// caller's tenant ID. Operations return an error when invoked without
// a tenant ID on ctx — see package doc for rationale.
//
// When inner implements [cache.BulkCache], the returned value also
// implements BulkCache so MGet/MSet/SetNX are forwarded with tenant
// scoping rather than falling back to the per-key Cache helpers.
//
// Wrap panics on a nil inner cache; callers should treat a nil inner
// as a programming error and the wrapper makes that explicit upfront.
func Wrap(inner cache.Cache) cache.Cache {
	if inner == nil {
		panic("cache/tenant: Wrap inner cache must not be nil")
	}
	if bulk, ok := inner.(cache.BulkCache); ok {
		return &scopedBulk{scoped: scoped{inner: inner}, bulk: bulk}
	}
	return &scoped{inner: inner}
}

// scopedKey rewrites raw with the kit-canonical tenant key format. It validates
// the caller-provided key before adding the tenant prefix so empty raw
// keys cannot be hidden by the wrapper.
func scopedKey(ctx context.Context, raw string) (string, error) {
	if err := cache.ValidateKey(raw); err != nil {
		return "", err
	}
	id, err := coretenant.Required(ctx)
	if err != nil {
		return "", redact.WrapError("cache/tenant", err)
	}
	scoped, err := coretenant.KeyFor(id, raw)
	if err != nil {
		return "", err
	}
	if err := cache.ValidateKey(scoped); err != nil {
		return "", err
	}
	return scoped, nil
}

// Get rewrites the key and delegates.
func (s *scoped) Get(ctx context.Context, key string) ([]byte, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	scoped, err := scopedKey(ctx, key)
	if err != nil {
		return nil, err
	}
	return s.inner.Get(ctx, scoped)
}

// Set rewrites the key and delegates. ttl semantics are unchanged.
func (s *scoped) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	if err := s.ready(); err != nil {
		return err
	}
	scoped, err := scopedKey(ctx, key)
	if err != nil {
		return err
	}
	return s.inner.Set(ctx, scoped, value, ttl)
}

// Delete rewrites the key and delegates.
func (s *scoped) Delete(ctx context.Context, key string) error {
	if err := s.ready(); err != nil {
		return err
	}
	scoped, err := scopedKey(ctx, key)
	if err != nil {
		return err
	}
	return s.inner.Delete(ctx, scoped)
}

// Exists rewrites the key and delegates.
func (s *scoped) Exists(ctx context.Context, key string) (bool, error) {
	if err := s.ready(); err != nil {
		return false, err
	}
	scoped, err := scopedKey(ctx, key)
	if err != nil {
		return false, err
	}
	return s.inner.Exists(ctx, scoped)
}

// MGet rewrites every key and forwards a single bulk read to the inner
// BulkCache. The result map is rebuilt with the caller's raw keys so
// scoping is invisible to consumers.
func (s *scopedBulk) MGet(ctx context.Context, keys []string) (map[string][]byte, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	if len(keys) == 0 {
		return map[string][]byte{}, nil
	}
	if err := cache.ValidateBulkKeys(keys); err != nil {
		return nil, err
	}
	scoped := make([]string, len(keys))
	rawByScoped := make(map[string]string, len(keys))
	for i, k := range keys {
		sk, err := scopedKey(ctx, k)
		if err != nil {
			return nil, err
		}
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
	if err := s.ready(); err != nil {
		return err
	}
	if len(items) == 0 {
		return nil
	}
	if err := cache.ValidateBulkItems(items); err != nil {
		return err
	}
	scoped := make(map[string][]byte, len(items))
	for k, v := range items {
		sk, err := scopedKey(ctx, k)
		if err != nil {
			return err
		}
		scoped[sk] = v
	}
	return s.bulk.MSet(ctx, scoped, ttl)
}

// SetNX rewrites the key and forwards to the inner BulkCache so that
// cross-process compute-once semantics survive tenant scoping. Without
// this, the [cache.SetNX] free function falls back to a racy
// Exists+Set sequence.
func (s *scopedBulk) SetNX(ctx context.Context, key string, value []byte, ttl time.Duration) (bool, error) {
	if err := s.ready(); err != nil {
		return false, err
	}
	scoped, err := scopedKey(ctx, key)
	if err != nil {
		return false, err
	}
	return s.bulk.SetNX(ctx, scoped, value, ttl)
}

func (s *scoped) ready() error {
	if s == nil || s.inner == nil {
		return cache.ErrInvalidCache
	}
	return nil
}

func (s *scopedBulk) ready() error {
	if s == nil || s.bulk == nil {
		return cache.ErrInvalidCache
	}
	return s.scoped.ready()
}
