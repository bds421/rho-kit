package session

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testRoot() []byte {
	return []byte("0123456789abcdef0123456789abcdef")
}

type memStore struct {
	ver  int
	role string
}

func (m *memStore) TokenVersion(context.Context, string) (int, error) {
	return m.ver, nil
}
func (m *memStore) Role(context.Context, string) (string, error) {
	return m.role, nil
}

func TestMintVerify_RoundTrip(t *testing.T) {
	signer, err := NewSigner(testRoot(), "session")
	require.NoError(t, err)

	now := time.Date(2026, 6, 27, 12, 0, 0, 0, time.UTC)
	exp := now.Add(time.Hour)
	token, err := signer.Mint(Claims{
		UserID:       "user-1",
		Tenant:       "tenant-a",
		Role:         "member",
		TokenVersion: 3,
		Exp:          exp,
	})
	require.NoError(t, err)

	got, err := signer.Verify(token, now)
	require.NoError(t, err)
	assert.Equal(t, "user-1", got.UserID)
	assert.Equal(t, "tenant-a", got.Tenant)
	assert.Equal(t, "member", got.Role)
	assert.Equal(t, 3, got.TokenVersion)
}

func TestVerify_RejectsExpiredToken(t *testing.T) {
	signer, err := NewSigner(testRoot(), "session")
	require.NoError(t, err)

	now := time.Date(2026, 6, 27, 12, 0, 0, 0, time.UTC)
	token, err := signer.Mint(Claims{
		UserID: "user-1", Tenant: "t", Role: "member",
		TokenVersion: 1, Exp: now.Add(-time.Minute),
	})
	require.NoError(t, err)

	_, err = signer.Verify(token, now)
	assert.ErrorIs(t, err, ErrExpired)
}

func TestValidator_RejectsRevokedVersion(t *testing.T) {
	signer, err := NewSigner(testRoot(), "session")
	require.NoError(t, err)
	now := time.Date(2026, 6, 27, 12, 0, 0, 0, time.UTC)
	token, err := signer.Mint(Claims{
		UserID: "user-1", Tenant: "t", Role: "member",
		TokenVersion: 1, Exp: now.Add(time.Hour),
	})
	require.NoError(t, err)

	v := Validator{Signer: signer, Store: &memStore{ver: 2, role: "member"}}
	_, err = v.Validate(context.Background(), token, now)
	assert.ErrorIs(t, err, ErrSessionRevoked)
}

func TestValidator_RefreshesRoleFromStore(t *testing.T) {
	signer, err := NewSigner(testRoot(), "session")
	require.NoError(t, err)
	now := time.Date(2026, 6, 27, 12, 0, 0, 0, time.UTC)
	token, err := signer.Mint(Claims{
		UserID: "user-1", Tenant: "t", Role: "admin",
		TokenVersion: 1, Exp: now.Add(time.Hour),
	})
	require.NoError(t, err)

	v := Validator{Signer: signer, Store: &memStore{ver: 1, role: "viewer"}}
	claims, err := v.Validate(context.Background(), token, now)
	require.NoError(t, err)
	assert.Equal(t, "viewer", claims.Role)
}

func TestDeriveKey_DistinctLabels(t *testing.T) {
	a, err := DeriveKey(testRoot(), "session")
	require.NoError(t, err)
	b, err := DeriveKey(testRoot(), "audit")
	require.NoError(t, err)
	assert.NotEqual(t, a, b)
}