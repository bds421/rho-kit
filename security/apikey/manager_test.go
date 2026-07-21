package apikey

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/core/v2/apperror"
)

func fixedClock(at time.Time) func() time.Time {
	return func() time.Time { return at }
}

// mutableClock is a clock whose current time can be advanced between calls,
// letting a single Manager observe issuance and rotation at different instants.
type mutableClock struct{ at time.Time }

func (c *mutableClock) now() time.Time { return c.at }

func TestNewManager_PanicsOnNilRepo(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatalf("expected panic on nil repo")
		}
	}()
	_ = NewManager(nil)
}

func TestManager_IssueStoresAndReturnsToken(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC)
	repo := NewMemoryRepository()
	m := NewManager(repo, WithClock(fixedClock(now)))

	key, token, err := m.Issue(ctx, IssueOptions{Owner: "o", Scopes: []string{"s"}})
	require.NoError(t, err)
	assert.Equal(t, now, key.CreatedAt)

	stored, err := repo.FindByID(ctx, key.ID)
	require.NoError(t, err)
	assert.Equal(t, key.Hash, stored.Hash)

	id, secretSeg, err := Parse(token.RevealString(), DefaultPrefix)
	require.NoError(t, err)
	assert.Equal(t, key.ID, id)
	assert.NoError(t, stored.Verify(secretSeg, now))
}

func TestManager_RevokeImmediately(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC)
	repo := NewMemoryRepository()
	m := NewManager(repo, WithClock(fixedClock(now)))

	key, _, err := m.Issue(ctx, IssueOptions{Owner: "o"})
	require.NoError(t, err)
	require.NoError(t, m.Revoke(ctx, key.ID))

	stored, err := repo.FindByID(ctx, key.ID)
	require.NoError(t, err)
	assert.Equal(t, now, stored.RevokedAt)
	assert.False(t, stored.IsActive(now))
}

func TestManager_RotateKeepsOldValidDuringOverlap(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC)
	repo := NewMemoryRepository()
	m := NewManager(repo, WithClock(fixedClock(now)))

	oldKey, oldToken, err := m.Issue(ctx, IssueOptions{Owner: "o", Scopes: []string{"read"}})
	require.NoError(t, err)
	_, oldSecret, _ := Parse(oldToken.RevealString(), DefaultPrefix)

	overlap := time.Hour
	newKey, newToken, err := m.Rotate(ctx, oldKey.ID, overlap)
	require.NoError(t, err)

	// New key inherits owner + scopes and records the rotation source.
	assert.Equal(t, "o", newKey.Owner)
	assert.Equal(t, []string{"read"}, newKey.Scopes)
	assert.Equal(t, oldKey.ID, newKey.RotatedFrom)

	// Old key still verifies during the overlap window...
	storedOld, err := repo.FindByID(ctx, oldKey.ID)
	require.NoError(t, err)
	assert.NoError(t, storedOld.Verify(oldSecret, now))
	assert.NoError(t, storedOld.Verify(oldSecret, now.Add(59*time.Minute)))

	// ...and stops once the window closes.
	assert.ErrorIs(t, storedOld.Verify(oldSecret, now.Add(overlap)), ErrRevoked)

	// New key works immediately and keeps working past the overlap.
	_, newSecret, _ := Parse(newToken.RevealString(), DefaultPrefix)
	storedNew, err := repo.FindByID(ctx, newKey.ID)
	require.NoError(t, err)
	assert.NoError(t, storedNew.Verify(newSecret, now))
	assert.NoError(t, storedNew.Verify(newSecret, now.Add(2*overlap)))
}

func TestManager_RotateZeroOverlapRevokesImmediately(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC)
	repo := NewMemoryRepository()
	m := NewManager(repo, WithClock(fixedClock(now)))

	oldKey, oldToken, err := m.Issue(ctx, IssueOptions{Owner: "o"})
	require.NoError(t, err)
	_, oldSecret, _ := Parse(oldToken.RevealString(), DefaultPrefix)

	_, _, err = m.Rotate(ctx, oldKey.ID, 0)
	require.NoError(t, err)

	storedOld, err := repo.FindByID(ctx, oldKey.ID)
	require.NoError(t, err)
	assert.ErrorIs(t, storedOld.Verify(oldSecret, now), ErrRevoked)
}

func TestManager_RotateMissingKeyErrors(t *testing.T) {
	repo := NewMemoryRepository()
	m := NewManager(repo)
	_, _, err := m.Rotate(context.Background(), "missing", time.Hour)
	assert.Error(t, err)
}

func TestManager_List(t *testing.T) {
	ctx := context.Background()
	repo := NewMemoryRepository()
	m := NewManager(repo)
	for i := 0; i < 2; i++ {
		_, _, err := m.Issue(ctx, IssueOptions{Owner: "owner-a"})
		require.NoError(t, err)
	}
	_, _, err := m.Issue(ctx, IssueOptions{Owner: "owner-b"})
	require.NoError(t, err)

	list, err := m.List(ctx, "owner-a")
	require.NoError(t, err)
	assert.Len(t, list, 2)
}

func TestManager_RotatePreservesPrefix(t *testing.T) {
	ctx := context.Background()
	repo := NewMemoryRepository()
	m := NewManager(repo)
	oldKey, _, err := m.Issue(ctx, IssueOptions{Owner: "o", Prefix: "acme"})
	require.NoError(t, err)
	newKey, _, err := m.Rotate(ctx, oldKey.ID, time.Hour)
	require.NoError(t, err)
	assert.Equal(t, "acme", prefixOf(newKey))
}

func TestManager_RotateExpiry(t *testing.T) {
	ctx := context.Background()
	issuedAt := time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC)
	ttl := 24 * time.Hour

	tests := []struct {
		name        string
		issueExpiry time.Time // zero means the old key never expires
		rotateAt    time.Time
		overlap     time.Duration
	}{
		{
			name:        "near expiry",
			issueExpiry: issuedAt.Add(ttl),
			rotateAt:    issuedAt.Add(ttl - time.Minute),
			overlap:     time.Hour,
		},
		{
			name:        "after expiry",
			issueExpiry: issuedAt.Add(ttl),
			rotateAt:    issuedAt.Add(ttl + time.Hour),
			overlap:     time.Hour,
		},
		{
			name:        "no expiry stays unbounded",
			issueExpiry: time.Time{},
			rotateAt:    issuedAt.Add(ttl),
			overlap:     time.Hour,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			clk := &mutableClock{at: issuedAt}
			repo := NewMemoryRepository()
			m := NewManager(repo, WithClock(clk.now))

			oldKey, _, err := m.Issue(ctx, IssueOptions{Owner: "o", ExpiresAt: tc.issueExpiry})
			require.NoError(t, err)

			clk.at = tc.rotateAt
			newKey, newToken, err := m.Rotate(ctx, oldKey.ID, tc.overlap)
			require.NoError(t, err)

			_, newSecret, _ := Parse(newToken.RevealString(), DefaultPrefix)
			storedNew, err := repo.FindByID(ctx, newKey.ID)
			require.NoError(t, err)

			// The freshly minted replacement must be usable at rotation time and
			// must outlive the old key's overlap window — otherwise rotation
			// produces a dead or near-dead key, defeating the overlap.
			require.NoError(t, storedNew.Verify(newSecret, tc.rotateAt),
				"new key must verify at rotation time")
			assert.NoError(t, storedNew.Verify(newSecret, tc.rotateAt.Add(tc.overlap+time.Minute)),
				"new key must still verify after the old key's overlap window closes")

			if tc.issueExpiry.IsZero() {
				assert.True(t, storedNew.ExpiresAt.IsZero(),
					"a non-expiring source key must rotate to a non-expiring key")
			}
		})
	}
}

func TestManager_RotateRejectsAlreadyRevoked(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC)
	repo := NewMemoryRepository()
	m := NewManager(repo, WithClock(fixedClock(now)))

	key, _, err := m.Issue(ctx, IssueOptions{Owner: "o"})
	require.NoError(t, err)
	require.NoError(t, m.Revoke(ctx, key.ID))

	_, _, err = m.Rotate(ctx, key.ID, time.Hour)
	assert.ErrorIs(t, err, ErrRevoked)
}

func TestRotatedExpiry_ZeroCreatedAtFallsBackToAbsolute(t *testing.T) {
	now := time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC)
	exp := now.Add(24 * time.Hour)
	got := rotatedExpiry(Key{ExpiresAt: exp}, now) // CreatedAt zero
	assert.Equal(t, exp, got, "zero CreatedAt must not yield multi-century TTL")
}

func TestMemoryRepository_RevokeCanMoveEarlier(t *testing.T) {
	ctx := context.Background()
	repo := NewMemoryRepository()
	now := time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC)
	key, _, err := Generate(GenerateOptions{Owner: "o", Now: now})
	require.NoError(t, err)
	require.NoError(t, repo.Insert(ctx, key))

	future := now.Add(time.Hour)
	require.NoError(t, repo.Revoke(ctx, key.ID, future))
	require.NoError(t, repo.Revoke(ctx, key.ID, now)) // emergency pull-in

	stored, err := repo.FindByID(ctx, key.ID)
	require.NoError(t, err)
	assert.Equal(t, now, stored.RevokedAt)
}

// revokeFailRepo fails Revoke for a specific ID so Rotate's compensation
// path can be exercised without a real storage outage.
type revokeFailRepo struct {
	*MemoryRepository
	failID string
	// revoked tracks IDs that successfully received Revoke after the
	// intentional failure window (used to observe compensation).
	revoked []string
}

func (r *revokeFailRepo) Revoke(ctx context.Context, id string, at time.Time) error {
	if id == r.failID {
		return errors.New("repo revoke outage")
	}
	r.revoked = append(r.revoked, id)
	return r.MemoryRepository.Revoke(ctx, id, at)
}

// TestManager_RotateRevokeFailureCleansOrphan pins best-effort cleanup:
// when scheduling old-key revocation fails after the replacement was
// inserted, the new key must be revoked so a discarded token does not
// leave a live orphaned credential.
func TestManager_RotateRevokeFailureCleansOrphan(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC)
	base := NewMemoryRepository()
	// Issue old key first so we know its ID.
	mTmp := NewManager(base, WithClock(fixedClock(now)))
	oldKey, _, err := mTmp.Issue(ctx, IssueOptions{Owner: "o", Scopes: []string{"read"}})
	require.NoError(t, err)

	repo := &revokeFailRepo{MemoryRepository: base, failID: oldKey.ID}
	m := NewManager(repo, WithClock(fixedClock(now)))

	newKey, token, err := m.Rotate(ctx, oldKey.ID, time.Hour)
	require.Error(t, err)
	assert.Nil(t, token)
	assert.Empty(t, newKey.ID)

	// The replacement must have been inserted then best-effort revoked.
	// Scan all keys for a RotatedFrom=old and ensure it is revoked now.
	all, err := repo.ListByOwner(ctx, "o")
	require.NoError(t, err)
	var foundReplacement bool
	for _, k := range all {
		if k.RotatedFrom == oldKey.ID {
			foundReplacement = true
			assert.False(t, k.RevokedAt.IsZero(), "orphan replacement must be revoked")
			assert.False(t, k.IsActive(now), "orphan must not verify as active")
		}
	}
	assert.True(t, foundReplacement, "replacement key should have been inserted before revoke failure")
	// Old key must remain unrevoked (Revoke on oldID failed).
	storedOld, err := repo.FindByID(ctx, oldKey.ID)
	require.NoError(t, err)
	assert.True(t, storedOld.RevokedAt.IsZero(), "old key stays active when schedule-revoke fails")
}

func TestManager_RotateOwned_RejectsWrongOwner(t *testing.T) {
	repo := NewMemoryRepository()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	m := NewManager(repo, WithClock(func() time.Time { return now }))
	ctx := context.Background()

	key, _, err := m.Issue(ctx, IssueOptions{Owner: "owner-a"})
	require.NoError(t, err)

	_, _, err = m.RotateOwned(ctx, "owner-b", key.ID, time.Hour)
	require.Error(t, err)
	assert.True(t, apperror.IsNotFound(err), "wrong owner must look like not-found, got %v", err)

	// Original key still active.
	stored, err := repo.FindByID(ctx, key.ID)
	require.NoError(t, err)
	assert.True(t, stored.RevokedAt.IsZero())
}

func TestManager_RotateOwned_Success(t *testing.T) {
	repo := NewMemoryRepository()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	m := NewManager(repo, WithClock(func() time.Time { return now }))
	ctx := context.Background()

	oldKey, _, err := m.Issue(ctx, IssueOptions{Owner: "owner-a", Scopes: []string{"read"}})
	require.NoError(t, err)

	newKey, newToken, err := m.RotateOwned(ctx, "owner-a", oldKey.ID, time.Hour)
	require.NoError(t, err)
	require.NotNil(t, newToken)
	assert.Equal(t, "owner-a", newKey.Owner)
}

func TestManager_RevokeOwned_RejectsWrongOwner(t *testing.T) {
	repo := NewMemoryRepository()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	m := NewManager(repo, WithClock(func() time.Time { return now }))
	ctx := context.Background()

	key, _, err := m.Issue(ctx, IssueOptions{Owner: "owner-a"})
	require.NoError(t, err)

	err = m.RevokeOwned(ctx, "owner-b", key.ID)
	require.Error(t, err)

	stored, err := repo.FindByID(ctx, key.ID)
	require.NoError(t, err)
	assert.True(t, stored.RevokedAt.IsZero(), "wrong-owner revoke must not mutate")
}
