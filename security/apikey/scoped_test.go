package apikey

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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
