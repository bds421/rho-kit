package apikey

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGenerate_ProducesVerifiableToken(t *testing.T) {
	now := time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC)
	key, token, err := Generate(GenerateOptions{
		Kind:   KindAPI,
		Scopes: []string{"orders.read"},
		Owner:  "tenant-1",
		Now:    now,
	})
	require.NoError(t, err)
	require.NotNil(t, token)

	full := token.RevealString()
	parts := strings.Split(full, "_")
	require.Len(t, parts, 3, "token has prefix_id_secret shape")
	assert.Equal(t, DefaultPrefix, parts[0])
	assert.Equal(t, key.ID, parts[1])

	id, secretSeg, err := Parse(full, DefaultPrefix)
	require.NoError(t, err)
	assert.Equal(t, key.ID, id)
	assert.NoError(t, key.Verify(secretSeg, now))
}

func TestGenerate_DefaultsKindAndPrefix(t *testing.T) {
	key, token, err := Generate(GenerateOptions{Owner: "t"})
	require.NoError(t, err)
	assert.Equal(t, KindAPI, key.Kind)
	assert.True(t, strings.HasPrefix(token.RevealString(), DefaultPrefix+"_"))
	assert.True(t, strings.HasPrefix(key.Prefix, DefaultPrefix+"_"))
}

func TestGenerate_RejectsInvalidKindAndPrefix(t *testing.T) {
	_, _, err := Generate(GenerateOptions{Kind: Kind("bogus")})
	assert.Error(t, err)

	_, _, err = Generate(GenerateOptions{Prefix: "Has_Caps"})
	assert.Error(t, err)

	_, _, err = Generate(GenerateOptions{Prefix: "waytoolongprefix"})
	assert.Error(t, err)
}

func TestGenerate_SecretNotRecoverableFromKey(t *testing.T) {
	key, token, err := Generate(GenerateOptions{})
	require.NoError(t, err)
	_, secretSeg, err := Parse(token.RevealString(), DefaultPrefix)
	require.NoError(t, err)
	// The stored hash must not equal the secret bytes.
	assert.NotEqual(t, []byte(secretSeg), key.Hash[:])
	assert.Equal(t, Hash(secretSeg), key.Hash)
}

func TestGenerate_FreshSecretEachCall(t *testing.T) {
	_, t1, err := Generate(GenerateOptions{})
	require.NoError(t, err)
	_, t2, err := Generate(GenerateOptions{})
	require.NoError(t, err)
	assert.NotEqual(t, t1.RevealString(), t2.RevealString())
}

func TestVerify_RejectsWrongSecret(t *testing.T) {
	key, _, err := Generate(GenerateOptions{})
	require.NoError(t, err)
	assert.ErrorIs(t, key.Verify("not-the-secret", time.Now()), ErrInvalidSecret)
}

func TestVerify_RejectsExpired(t *testing.T) {
	now := time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC)
	key, token, err := Generate(GenerateOptions{Now: now, ExpiresAt: now.Add(time.Hour)})
	require.NoError(t, err)
	_, secretSeg, _ := Parse(token.RevealString(), DefaultPrefix)

	assert.NoError(t, key.Verify(secretSeg, now.Add(59*time.Minute)))
	assert.ErrorIs(t, key.Verify(secretSeg, now.Add(time.Hour)), ErrExpired)
	assert.ErrorIs(t, key.Verify(secretSeg, now.Add(2*time.Hour)), ErrExpired)
}

func TestVerify_RejectsRevoked(t *testing.T) {
	now := time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC)
	key, token, err := Generate(GenerateOptions{Now: now})
	require.NoError(t, err)
	_, secretSeg, _ := Parse(token.RevealString(), DefaultPrefix)

	key.RevokedAt = now.Add(time.Minute)
	assert.NoError(t, key.Verify(secretSeg, now), "active before revocation")
	assert.ErrorIs(t, key.Verify(secretSeg, now.Add(time.Minute)), ErrRevoked)
}

func TestVerify_RevocationTakesPrecedenceOverExpiry(t *testing.T) {
	now := time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC)
	key, token, err := Generate(GenerateOptions{Now: now, ExpiresAt: now.Add(time.Hour)})
	require.NoError(t, err)
	_, secretSeg, _ := Parse(token.RevealString(), DefaultPrefix)
	key.RevokedAt = now.Add(time.Minute)
	// Past both revocation and expiry: revoked is the reported reason.
	assert.ErrorIs(t, key.Verify(secretSeg, now.Add(2*time.Hour)), ErrRevoked)
}

func TestParse_RejectsMalformed(t *testing.T) {
	cases := []string{
		"",
		"rho",
		"rho_only",
		"rho__emptysecret",
		"rho_id_",
		"_id_secret",
		"wrong_id_secret",
		"rho_id_secret_extra",
	}
	for _, tc := range cases {
		t.Run(tc, func(t *testing.T) {
			_, _, err := Parse(tc, DefaultPrefix)
			assert.ErrorIs(t, err, ErrMalformedToken)
		})
	}
}

func TestParse_DefaultsPrefixWhenEmpty(t *testing.T) {
	_, _, err := Parse("rho_id_secret", "")
	assert.NoError(t, err)
}

func TestHash_Deterministic(t *testing.T) {
	a := Hash("same-input")
	b := Hash("same-input")
	assert.Equal(t, a, b)
	assert.NotEqual(t, a, Hash("different"))
}

func TestIsActive(t *testing.T) {
	now := time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC)
	active := Key{}
	assert.True(t, active.IsActive(now))
	assert.False(t, Key{ExpiresAt: now.Add(-time.Second)}.IsActive(now))
	assert.False(t, Key{RevokedAt: now.Add(-time.Second)}.IsActive(now))
}

func TestScopes_AreCopied(t *testing.T) {
	scopes := []string{"a", "b"}
	key, _, err := Generate(GenerateOptions{Scopes: scopes})
	require.NoError(t, err)
	scopes[0] = "mutated"
	assert.Equal(t, "a", key.Scopes[0], "Generate must copy the scopes slice")
}

func TestMemoryRepository_Lifecycle(t *testing.T) {
	ctx := context.Background()
	repo := NewMemoryRepository()
	now := time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC)

	key, _, err := Generate(GenerateOptions{Owner: "owner-1", Now: now})
	require.NoError(t, err)

	require.NoError(t, repo.Insert(ctx, key))

	// Duplicate insert conflicts.
	assert.Error(t, repo.Insert(ctx, key))

	got, err := repo.FindByID(ctx, key.ID)
	require.NoError(t, err)
	assert.Equal(t, key.ID, got.ID)

	// Not found.
	_, err = repo.FindByID(ctx, "missing")
	assert.Error(t, err)

	// Revoke is reflected and idempotent.
	require.NoError(t, repo.Revoke(ctx, key.ID, now))
	require.NoError(t, repo.Revoke(ctx, key.ID, now.Add(time.Hour)))
	got, err = repo.FindByID(ctx, key.ID)
	require.NoError(t, err)
	assert.Equal(t, now, got.RevokedAt, "first revocation time is retained")

	assert.Error(t, repo.Revoke(ctx, "missing", now))

	list, err := repo.ListByOwner(ctx, "owner-1")
	require.NoError(t, err)
	assert.Len(t, list, 1)
	empty, err := repo.ListByOwner(ctx, "owner-2")
	require.NoError(t, err)
	assert.Empty(t, empty)
}

func TestMemoryRepository_ReturnsCopies(t *testing.T) {
	ctx := context.Background()
	repo := NewMemoryRepository()
	key, _, err := Generate(GenerateOptions{Owner: "o", Scopes: []string{"x"}})
	require.NoError(t, err)
	require.NoError(t, repo.Insert(ctx, key))

	got, err := repo.FindByID(ctx, key.ID)
	require.NoError(t, err)
	got.Scopes[0] = "mutated"

	again, err := repo.FindByID(ctx, key.ID)
	require.NoError(t, err)
	assert.Equal(t, "x", again.Scopes[0], "stored key must not be mutated by callers")
}

func TestErrorsAreDistinct(t *testing.T) {
	all := []error{ErrMalformedToken, ErrInvalidSecret, ErrExpired, ErrRevoked}
	for i := range all {
		for j := range all {
			if i != j {
				assert.False(t, errors.Is(all[i], all[j]))
			}
		}
	}
}
