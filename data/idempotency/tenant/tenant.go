// Package tenant provides a tenant-scoped wrapper around any
// [idempotency.Store]. Every idempotency key is rewritten with
// [coretenant.Key] before reaching the underlying backend, so the same
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
// reported as an error from Store operations — callers that may
// legitimately run outside a tenant scope should keep using the bare
// store.
//
// # Deployment rule: do not share a backend keyspace with a bare store
//
// Scoped keys use the kit-canonical length-prefixed format from
// [coretenant.KeyFor] (e.g. "tenant:1:a:3:foo"). That string is itself a
// valid raw [idempotency.ValidateKey] input, so a deployment that wires
// BOTH this wrapper AND a bare [idempotency.Store] against the same
// Redis prefix / Postgres table lets a bare-path caller who controls the
// raw key address another tenant's slot (Get replay, TryLock squat, Set
// poisoning). Keep tenant-wrapped and bare stores on disjoint backends
// or key prefixes; never mount both on one keyspace.
package tenant

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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

// Wrap returns an [idempotency.Store] that prefixes every key with
// the caller's tenant ID. Operations return an error when invoked
// without a tenant ID on ctx — see package doc for rationale.
//
// Wrap panics on a nil inner store.
func Wrap(inner idempotency.Store) idempotency.Store {
	if inner == nil {
		panic("idempotency/tenant: Wrap inner store must not be nil")
	}
	return &scoped{inner: inner}
}

// scopedKey rewrites raw with the kit-canonical tenant key format. It validates
// the caller-provided key before adding the tenant prefix so empty raw
// keys cannot be hidden by the wrapper.
//
// When the length-prefixed scoped form exceeds [idempotency.MaxKeyLen]
// (possible with long tenant IDs + long raw keys), the storage key is
// replaced with a fixed-length SHA-256 hex digest so every valid raw
// key remains usable regardless of tenant-ID length (review-12). Short
// scoped keys keep the readable length-prefixed form for operator
// introspection.
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
	if len(scoped) > idempotency.MaxKeyLen {
		sum := sha256.Sum256([]byte(scoped))
		scoped = hex.EncodeToString(sum[:])
	}
	if err := idempotency.ValidateKey(scoped); err != nil {
		return "", err
	}
	return scoped, nil
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
