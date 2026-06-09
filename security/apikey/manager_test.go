package apikey

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func fixedClock(at time.Time) func() time.Time {
	return func() time.Time { return at }
}

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
