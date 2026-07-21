package apikey

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"

	"github.com/bds421/rho-kit/crypto/v2/passhash"
)

func fastHashParams() passhash.Params {
	return passhash.Params{
		Memory: 8 * 1024, Iterations: 1, Parallelism: 1, SaltLen: 16, KeyLen: 32,
	}
}

const testSubjectUserID = "11111111-2222-3333-4444-555555555555"

func TestScopedResolver_ResolveSubjectUserID(t *testing.T) {
	now := time.Date(2026, 6, 27, 12, 0, 0, 0, time.UTC)
	key, token, err := GenerateScoped(ScopedGenerateOptions{
		Tenant: "tenant-a", Role: "member", SubjectUserID: testSubjectUserID,
		Scopes: []string{"read:contacts"}, Now: now, HashParams: fastHashParams(),
	})
	require.NoError(t, err)

	repo := NewMemoryPrefixRepository()
	require.NoError(t, repo.InsertScoped(context.Background(), key))

	resolver := NewScopedResolver(repo, ScopedTokenPrefixAPI, WithScopedClock(func() time.Time { return now }))
	p, err := resolver.Resolve(context.Background(), token.RevealString())
	require.NoError(t, err)
	assert.Equal(t, testSubjectUserID, p.UserID)
	assert.Equal(t, "tenant-a", p.Tenant)
	assert.True(t, HasScope(p, "read:contacts"))
}

func TestGenerateScoped_AllowsEmptySubjectUserID(t *testing.T) {
	key, _, err := GenerateScoped(ScopedGenerateOptions{
		Tenant: "tenant-a", Now: time.Now(), HashParams: fastHashParams(),
	})
	require.NoError(t, err)
	assert.Empty(t, key.SubjectUserID)
}

func TestGenerateScoped_RejectsNonUUIDSubjectUserID(t *testing.T) {
	_, _, err := GenerateScoped(ScopedGenerateOptions{
		Tenant: "tenant-a", SubjectUserID: "not-a-uuid",
		Now: time.Now(), HashParams: fastHashParams(),
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "UUID")
}

func TestScopedResolver_UnboundSubjectUserID(t *testing.T) {
	now := time.Now()
	key, token, err := GenerateScoped(ScopedGenerateOptions{
		Tenant: "tenant-a", Now: now, HashParams: fastHashParams(),
	})
	require.NoError(t, err)
	repo := NewMemoryPrefixRepository()
	require.NoError(t, repo.InsertScoped(context.Background(), key))
	resolver := NewScopedResolver(repo, ScopedTokenPrefixAPI, WithScopedClock(func() time.Time { return now }))

	p, err := resolver.Resolve(context.Background(), token.RevealString())
	require.NoError(t, err)
	assert.Empty(t, p.UserID)
	assert.Equal(t, "tenant-a", p.Tenant)
	assert.NotEmpty(t, p.KeyID)
}

func TestScopedResolver_RejectsWrongSecret(t *testing.T) {
	now := time.Now()
	key, token, err := GenerateScoped(ScopedGenerateOptions{
		Tenant: "t", SubjectUserID: testSubjectUserID,
		Now: now, HashParams: fastHashParams(),
	})
	require.NoError(t, err)
	repo := NewMemoryPrefixRepository()
	require.NoError(t, repo.InsertScoped(context.Background(), key))
	resolver := NewScopedResolver(repo, ScopedTokenPrefixAPI)

	_, err = resolver.Resolve(context.Background(), token.RevealString()+"tampered")
	assert.Error(t, err)
}

// TestMemoryPrefixRepository_ConcurrentSafeAndReturnsClone is the
// regression pin for review MEDIUM: MemoryPrefixRepository must be safe
// for concurrent InsertScoped/ActiveByPrefix, and ActiveByPrefix must
// return a defensive clone so callers cannot mutate stored Scopes/Hash.
func TestMemoryPrefixRepository_ConcurrentSafeAndReturnsClone(t *testing.T) {
	repo := NewMemoryPrefixRepository()
	key, _, err := GenerateScoped(ScopedGenerateOptions{
		Tenant: "t", Role: "r", SubjectUserID: testSubjectUserID,
		Scopes: []string{"read:a", "write:b"}, Now: time.Now(), HashParams: fastHashParams(),
	})
	require.NoError(t, err)
	require.NoError(t, repo.InsertScoped(context.Background(), key))

	// Clone: mutate returned Scopes/Hash must not affect subsequent reads.
	got, err := repo.ActiveByPrefix(context.Background(), key.Prefix)
	require.NoError(t, err)
	require.NotEmpty(t, got.Scopes)
	require.NotEmpty(t, got.Hash)
	got.Scopes[0] = "MUTATED"
	got.Hash[0] ^= 0xff

	again, err := repo.ActiveByPrefix(context.Background(), key.Prefix)
	require.NoError(t, err)
	assert.Equal(t, "read:a", again.Scopes[0], "stored Scopes must not be alias-mutated")
	assert.Equal(t, key.Hash[0], again.Hash[0], "stored Hash must not be alias-mutated")

	// Concurrent readers/writers must not race (run with -race).
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if i%4 == 0 {
				k, _, gerr := GenerateScoped(ScopedGenerateOptions{
					Tenant: "t", Role: "r", SubjectUserID: testSubjectUserID,
					Scopes: []string{"s"}, Now: time.Now(), HashParams: fastHashParams(),
				})
				if gerr != nil {
					return
				}
				_ = repo.InsertScoped(context.Background(), k) // may conflict; fine
			} else {
				_, _ = repo.ActiveByPrefix(context.Background(), key.Prefix)
			}
		}(i)
	}
	wg.Wait()
}

func TestScopedResolver_NeedsRehashForBcrypt(t *testing.T) {
	now := time.Now()
	key, token, err := GenerateScoped(ScopedGenerateOptions{
		Tenant: "t", SubjectUserID: testSubjectUserID,
		Now: now, HashParams: fastHashParams(),
	})
	require.NoError(t, err)

	parts := strings.Split(token.RevealString(), "_")
	require.Len(t, parts, 3)
	secretPart := parts[2]
	bcryptHash, err := bcrypt.GenerateFromPassword([]byte(secretPart), bcrypt.MinCost)
	require.NoError(t, err)
	key.Hash = bcryptHash

	repo := NewMemoryPrefixRepository()
	require.NoError(t, repo.InsertScoped(context.Background(), key))
	resolver := NewScopedResolver(repo, ScopedTokenPrefixAPI,
		WithScopedClock(func() time.Time { return now }),
		WithScopedHashTarget(fastHashParams()),
	)
	p, err := resolver.Resolve(context.Background(), token.RevealString())
	require.NoError(t, err)
	assert.True(t, p.NeedsRehash, "legacy bcrypt match must surface NeedsRehash for upgrade path")
}

func TestScopedResolver_NeedsRehashFalseForFreshArgon(t *testing.T) {
	now := time.Now()
	params := fastHashParams()
	key, token, err := GenerateScoped(ScopedGenerateOptions{
		Tenant: "t", SubjectUserID: testSubjectUserID,
		Now: now, HashParams: params,
	})
	require.NoError(t, err)
	repo := NewMemoryPrefixRepository()
	require.NoError(t, repo.InsertScoped(context.Background(), key))
	resolver := NewScopedResolver(repo, ScopedTokenPrefixAPI,
		WithScopedClock(func() time.Time { return now }),
		WithScopedHashTarget(params),
	)
	p, err := resolver.Resolve(context.Background(), token.RevealString())
	require.NoError(t, err)
	assert.False(t, p.NeedsRehash, "fresh argon2id under target params should not need rehash")
}
