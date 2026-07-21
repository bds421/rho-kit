package apikey

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/bds421/rho-kit/core/v2/apperror"
	"github.com/bds421/rho-kit/core/v2/secret"
)

// Manager is the issue/rotate/revoke lifecycle surface over a [Repository].
// It is the privileged side of the API-key feature — typically gated behind
// a root key or an authenticated operator — and is kept separate from
// verification so request-path code never imports issuance logic.
type Manager struct {
	repo Repository
	now  func() time.Time
}

// ManagerOption configures a [Manager].
type ManagerOption func(*Manager)

// WithClock overrides the wall clock used for issuance timestamps and
// scheduled revocation. Defaults to [time.Now].
func WithClock(now func() time.Time) ManagerOption {
	return func(m *Manager) {
		if now != nil {
			m.now = now
		}
	}
}

// NewManager returns a Manager backed by repo. It panics on a nil repo — a
// missing store is a fail-fast misconfiguration.
func NewManager(repo Repository, opts ...ManagerOption) *Manager {
	if repo == nil {
		panic("apikey: NewManager requires a non-nil Repository")
	}
	m := &Manager{repo: repo, now: time.Now}
	for _, opt := range opts {
		if opt != nil {
			opt(m)
		}
	}
	return m
}

// IssueOptions describes a key to mint. It mirrors the fields of
// [GenerateOptions] that a caller controls; CreatedAt is stamped from the
// Manager's clock.
type IssueOptions struct {
	Kind      Kind
	Scopes    []string
	Owner     string
	Prefix    string
	ExpiresAt time.Time
}

// Issue mints a new key, persists it, and returns the persistable record plus
// the one-time plaintext token. The caller must deliver the token to the
// owner; only the record (with the hash) is stored.
func (m *Manager) Issue(ctx context.Context, opts IssueOptions) (Key, *secret.String, error) {
	key, token, err := Generate(GenerateOptions{
		Kind:      opts.Kind,
		Scopes:    opts.Scopes,
		Owner:     opts.Owner,
		Prefix:    opts.Prefix,
		ExpiresAt: opts.ExpiresAt,
		Now:       m.now(),
	})
	if err != nil {
		return Key{}, nil, err
	}
	if err := m.repo.Insert(ctx, key); err != nil {
		return Key{}, nil, fmt.Errorf("apikey: issue: %w", err)
	}
	return key, token, nil
}

// Rotate issues a replacement for oldID and schedules the old key to expire
// after overlap, so in-flight clients have a window to adopt the new key
// before the old one stops working. The replacement inherits the old key's
// owner, kind, and scopes and records RotatedFrom = oldID. A zero or negative
// overlap revokes the old key immediately.
//
// The replacement does not inherit the old key's absolute ExpiresAt — that
// would yield a dead or near-dead key when rotating at or after expiry,
// defeating the overlap window. Instead it is granted a fresh lifetime equal
// to the old key's original lifetime (its ExpiresAt minus CreatedAt), measured
// from the rotation instant. A non-expiring source key rotates to a
// non-expiring replacement.
//
// It returns the new key record and its one-time token.
func (m *Manager) Rotate(ctx context.Context, oldID string, overlap time.Duration) (Key, *secret.String, error) {
	old, err := m.repo.FindByID(ctx, oldID)
	if err != nil {
		return Key{}, nil, fmt.Errorf("apikey: rotate: load old key: %w", err)
	}
	// A revoked key must not be resurrected into a fresh active credential;
	// callers that need a genuinely new credential after revoke should Issue.
	now := m.now()
	if !old.RevokedAt.IsZero() && !old.RevokedAt.After(now) {
		return Key{}, nil, fmt.Errorf("apikey: rotate: %w", ErrRevoked)
	}

	newKey, token, err := Generate(GenerateOptions{
		Kind:        old.Kind,
		Scopes:      old.Scopes,
		Owner:       old.Owner,
		Prefix:      prefixOf(old),
		ExpiresAt:   rotatedExpiry(old, now),
		RotatedFrom: oldID,
		Now:         now,
	})
	if err != nil {
		return Key{}, nil, err
	}
	if err := m.repo.Insert(ctx, newKey); err != nil {
		return Key{}, nil, fmt.Errorf("apikey: rotate: insert new key: %w", err)
	}

	// A future revocation time is exactly the overlap window: Verify treats
	// the old key as revoked only once now reaches RevokedAt.
	revokeAt := now
	if overlap > 0 {
		revokeAt = now.Add(overlap)
	}
	if err := m.repo.Revoke(ctx, oldID, revokeAt); err != nil {
		// Best-effort roll back the newly inserted key so a failed
		// revocation schedule does not leave an orphaned live credential
		// whose token was never returned to the caller.
		_ = m.repo.Revoke(ctx, newKey.ID, now)
		return Key{}, nil, fmt.Errorf("apikey: rotate: schedule old key revocation: %w", err)
	}
	return newKey, token, nil
}

// RotateOwned is owner-scoped [Rotate]: the key must exist and belong to
// owner. On owner mismatch the error is not-found (same as a missing id)
// so callers cannot probe other tenants' key ids.
func (m *Manager) RotateOwned(ctx context.Context, owner, oldID string, overlap time.Duration) (Key, *secret.String, error) {
	if owner == "" {
		return Key{}, nil, fmt.Errorf("apikey: rotate owned: owner must not be empty")
	}
	old, err := m.repo.FindByID(ctx, oldID)
	if err != nil {
		return Key{}, nil, fmt.Errorf("apikey: rotate owned: load old key: %w", err)
	}
	if old.Owner != owner {
		return Key{}, nil, apperror.NewNotFound("api key", oldID)
	}
	return m.Rotate(ctx, oldID, overlap)
}

// RevokeOwned is owner-scoped [Revoke]. Owner mismatch returns not-found.
func (m *Manager) RevokeOwned(ctx context.Context, owner, id string) error {
	if owner == "" {
		return fmt.Errorf("apikey: revoke owned: owner must not be empty")
	}
	key, err := m.repo.FindByID(ctx, id)
	if err != nil {
		return fmt.Errorf("apikey: revoke owned: %w", err)
	}
	if key.Owner != owner {
		return apperror.NewNotFound("api key", id)
	}
	return m.Revoke(ctx, id)
}

// Revoke revokes a key immediately.
func (m *Manager) Revoke(ctx context.Context, id string) error {
	if err := m.repo.Revoke(ctx, id, m.now()); err != nil {
		return fmt.Errorf("apikey: revoke: %w", err)
	}
	return nil
}

// List returns all keys owned by owner.
func (m *Manager) List(ctx context.Context, owner string) ([]Key, error) {
	keys, err := m.repo.ListByOwner(ctx, owner)
	if err != nil {
		return nil, fmt.Errorf("apikey: list: %w", err)
	}
	return keys, nil
}

// rotatedExpiry computes the replacement key's ExpiresAt during a rotation.
// A non-expiring source key (zero ExpiresAt) rotates to a non-expiring key.
// Otherwise the replacement is granted the source key's original lifetime
// (ExpiresAt minus CreatedAt) measured from now, so rotating at or after the
// old key's expiry still yields a fully usable replacement. When CreatedAt is
// unknown (a non-positive original lifetime) the absolute ExpiresAt is kept as
// a conservative fallback.
func rotatedExpiry(old Key, now time.Time) time.Time {
	if old.ExpiresAt.IsZero() {
		return time.Time{}
	}
	// Zero CreatedAt would make ExpiresAt.Sub(CreatedAt) ≈ ExpiresAt's
	// absolute Unix offset (~hundreds of years), granting a near-immortal
	// replacement. Fall back to the absolute ExpiresAt as documented.
	if old.CreatedAt.IsZero() {
		return old.ExpiresAt
	}
	ttl := old.ExpiresAt.Sub(old.CreatedAt)
	if ttl <= 0 {
		return old.ExpiresAt
	}
	return now.Add(ttl)
}

// prefixOf recovers the token prefix from a stored key's display prefix
// ("rho_018f0a3c" -> "rho"), so a rotated key keeps the original prefix.
// Falls back to [DefaultPrefix] when the stored value is unexpected.
func prefixOf(k Key) string {
	if i := strings.IndexByte(k.Prefix, '_'); i > 0 {
		return k.Prefix[:i]
	}
	return DefaultPrefix
}
