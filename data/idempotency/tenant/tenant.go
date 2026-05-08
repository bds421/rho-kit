// Package tenant provides a tenant-scoped wrapper around any
// [idempotency.Store]. Every idempotency key is prepended with
// "tenant:<id>:" before reaching the underlying backend, so the same
// raw key under two tenants resolves to two independent locks /
// cached responses.
//
// # Choice: namespace the key, not the fingerprint
//
// We had two options for closing cross-tenant idempotency replay:
//
//  1. Prepend the tenant ID to the storage key.
//  2. Mix the tenant ID into the body fingerprint.
//
// We chose (1) because:
//
//   - Full isolation in the storage layer. The same raw key in two
//     tenants never touches the same backend row. A backend bug that
//     ignores the fingerprint comparison still cannot leak between
//     tenants. Operators reading Redis MONITOR / Postgres rows see
//     tenants as distinct slots, not co-tenants of one slot.
//   - Predictable 422 semantics. Mixing the tenant into the
//     fingerprint would make "same key, same body, different tenant"
//     report the cached response as a fingerprint mismatch (422),
//     which is misleading — the request is *legitimately* a fresh
//     request in tenant B, not a body-mutation attack.
//   - Symmetry with the cache wrapper [cache/tenant]. Both wrappers
//     namespace the *key*; the operational mental model stays
//     consistent across the kit.
//
// The wrapper requires a tenant ID on the request context. Absence is
// a programming error and produces a panic from [tenant.Required] —
// callers that may legitimately run outside a tenant scope should
// keep using the bare store.
package tenant

import (
	"context"
	"strconv"
	"time"

	coretenant "github.com/bds421/rho-kit/core/v2/tenant"
	"github.com/bds421/rho-kit/data/v2/idempotency"
)

// keyPrefix is the namespace placed in front of every wrapped
// idempotency key. Mirrors the cache wrapper's layout for operator
// recognition.
const keyPrefix = "tenant:"

// scoped is the concrete wrapper. It rewrites every key on the way in
// and forwards the call to the underlying store unchanged.
type scoped struct {
	inner idempotency.Store
}

// Wrap returns an [idempotency.Store] that prefixes every key with
// the caller's tenant ID. The returned store panics on any operation
// invoked without a tenant ID on ctx — see package doc for rationale.
//
// Wrap panics on a nil inner store.
func Wrap(inner idempotency.Store) idempotency.Store {
	if inner == nil {
		panic("idempotency/tenant: inner store must not be nil")
	}
	return &scoped{inner: inner}
}

// scopedKey rewrites raw to "tenant:<len(id)>:<id>:<raw>". Panics if
// ctx carries no tenant ID.
//
// The length prefix prevents `tenant:"a:b" + key:"c"` from colliding
// with `tenant:"a" + key:"b:c"` when an ID happens to contain the ':'
// separator. [coretenant.NewID] rejects ':' as defence-in-depth, but
// callers can still construct `coretenant.ID` directly or via
// [coretenant.NewIDUnchecked]; the length prefix stays sound either way.
func scopedKey(ctx context.Context, raw string) string {
	id, err := coretenant.Required(ctx)
	if err != nil {
		panic("idempotency/tenant: " + err.Error())
	}
	s := string(id)
	return keyPrefix + strconv.Itoa(len(s)) + ":" + s + ":" + raw
}

// Get rewrites the key and delegates.
func (s *scoped) Get(ctx context.Context, key string, fingerprint []byte) (*idempotency.CachedResponse, bool, error) {
	return s.inner.Get(ctx, scopedKey(ctx, key), fingerprint)
}

// TryLock rewrites the key and delegates.
func (s *scoped) TryLock(ctx context.Context, key string, fingerprint []byte, ttl time.Duration) (string, bool, bool, error) {
	return s.inner.TryLock(ctx, scopedKey(ctx, key), fingerprint, ttl)
}

// Set rewrites the key and delegates.
func (s *scoped) Set(ctx context.Context, key, token string, resp idempotency.CachedResponse, ttl time.Duration) error {
	return s.inner.Set(ctx, scopedKey(ctx, key), token, resp, ttl)
}

// Unlock rewrites the key and delegates.
func (s *scoped) Unlock(ctx context.Context, key, token string) error {
	return s.inner.Unlock(ctx, scopedKey(ctx, key), token)
}
