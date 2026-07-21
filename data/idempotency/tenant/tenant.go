// Package tenant provides a tenant-scoped wrapper around any
// [idempotency.Store]. Every idempotency key is rewritten with
// [coretenant.KeyFor] and then stored as an opaque "tns:"+SHA-256 hex
// digest, so the same raw key under two tenants resolves to two
// independent locks / cached responses.
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
// reported as an error from Store operations — callers that may
// legitimately run outside a tenant scope should keep using the bare
// store.
//
// # Storage key unforgeability
//
// Scoped keys are always stored as:
//
//	tns: + hex(sha256(coretenant.KeyFor(tenant, raw)))
//
// [idempotency.ValidateKey] rejects both the reserved "tns:" prefix and the
// length-prefixed "tenant:…" form as user keys. Store backends accept the
// opaque form via [idempotency.ValidateStorageKey]. A bare store sharing a
// backend keyspace therefore cannot accept a caller-supplied key that
// addresses another tenant's slot by forging the readable length-prefixed
// shape (review-12). Storage keys are intentionally not human-readable.
package tenant

import (
	"context"
	"crypto/sha256"
	"time"

	"github.com/bds421/rho-kit/core/v2/redact"
	coretenant "github.com/bds421/rho-kit/core/v2/tenant"
	"github.com/bds421/rho-kit/data/v2/idempotency"
)

// scoped is the concrete wrapper. It rewrites every key on the way in
// and forwards the call to the underlying store unchanged.
type scoped struct {
	inner idempotency.Store
}

// Wrap returns an [idempotency.Store] that scopes every key to the
// caller's tenant ID via an opaque storage form. Operations return an
// error when invoked without a tenant ID on ctx — see package doc for
// rationale.
//
// Wrap panics on a nil inner store.
func Wrap(inner idempotency.Store) idempotency.Store {
	if inner == nil {
		panic("idempotency/tenant: Wrap inner store must not be nil")
	}
	return &scoped{inner: inner}
}

// scopedKey rewrites raw into the unforgeable tenant storage form. It
// validates the caller-provided key before scoping so empty/reserved raw
// keys cannot be hidden by the wrapper.
//
// The length-prefixed [coretenant.KeyFor] form is hashed to a fixed-size
// "tns:"+hex digest so (a) every valid raw key remains usable regardless
// of tenant-ID length, and (b) the on-disk key never matches a forgeable
// user-key shape accepted by [idempotency.ValidateKey].
func scopedKey(ctx context.Context, raw string) (string, error) {
	if err := idempotency.ValidateKey(raw); err != nil {
		return "", err
	}
	id, err := coretenant.Required(ctx)
	if err != nil {
		return "", redact.WrapError("idempotency/tenant", err)
	}
	scoped, err := coretenant.KeyFor(id, raw)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(scoped))
	storageKey := idempotency.FormatTenantStorageKey(sum[:])
	// Defence in depth: storage form must always pass the backend contract.
	if err := idempotency.ValidateStorageKey(storageKey); err != nil {
		return "", err
	}
	return storageKey, nil
}

// Get rewrites the key and delegates.
func (s *scoped) Get(ctx context.Context, key string, fingerprint []byte) (*idempotency.CachedResponse, bool, error) {
	if err := s.ready(); err != nil {
		return nil, false, err
	}
	scoped, err := scopedKey(ctx, key)
	if err != nil {
		return nil, false, err
	}
	return s.inner.Get(ctx, scoped, fingerprint)
}

// TryLock rewrites the key and delegates.
func (s *scoped) TryLock(ctx context.Context, key string, fingerprint []byte, ttl time.Duration) (string, bool, bool, error) {
	if err := s.ready(); err != nil {
		return "", false, false, err
	}
	scoped, err := scopedKey(ctx, key)
	if err != nil {
		return "", false, false, err
	}
	return s.inner.TryLock(ctx, scoped, fingerprint, ttl)
}

// Set rewrites the key and delegates.
func (s *scoped) Set(ctx context.Context, key, token string, resp idempotency.CachedResponse, ttl time.Duration) error {
	if err := s.ready(); err != nil {
		return err
	}
	scoped, err := scopedKey(ctx, key)
	if err != nil {
		return err
	}
	return s.inner.Set(ctx, scoped, token, resp, ttl)
}

// Unlock rewrites the key and delegates.
func (s *scoped) Unlock(ctx context.Context, key, token string) error {
	if err := s.ready(); err != nil {
		return err
	}
	scoped, err := scopedKey(ctx, key)
	if err != nil {
		return err
	}
	return s.inner.Unlock(ctx, scoped, token)
}

func (s *scoped) ready() error {
	if s == nil || s.inner == nil {
		return idempotency.ErrInvalidStore
	}
	return nil
}
