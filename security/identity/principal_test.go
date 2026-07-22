package identity

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMappingProfileProject_AllowListsAndNormalizes(t *testing.T) {
	p, err := (MappingProfile{
		TenantClaim:      "org_id",
		ActorClaim:       "client_id",
		Kind:             ActorOAuthClient,
		ScopesClaim:      "scope",
		PermissionsClaim: "permissions",
		Claims:           map[string]string{"email": "email"},
	}).Project("auth0|user-1", map[string]any{
		"org_id":      "org-1",
		"client_id":   "backend",
		"scope":       "orders:read orders:read",
		"permissions": []any{"orders:read", "orders:write"},
		"email":       "person@example.com",
		"unmapped":    "must-not-cross-boundary",
	})
	require.NoError(t, err)
	assert.Equal(t, "auth0|user-1", p.Subject)
	assert.Equal(t, "backend", p.Actor)
	assert.Equal(t, ActorOAuthClient, p.Kind)
	assert.Equal(t, "org-1", p.Tenant)
	assert.Equal(t, []string{"orders:read"}, p.Scopes)
	assert.Equal(t, []string{"orders:read", "orders:write"}, p.Permissions)
	assert.Equal(t, map[string]string{"email": "person@example.com"}, p.Claims)
}

func TestMappingProfileProject_FailsClosedOnSelectedClaimShape(t *testing.T) {
	_, err := (MappingProfile{TenantClaim: "org"}).Project("user-1", map[string]any{"org": []string{"bad"}})
	assert.ErrorIs(t, err, ErrInvalidPrincipal)
	_, err = (MappingProfile{ScopesClaim: "scope"}).Project("user-1", map[string]any{"scope": []any{"ok", 1}})
	assert.ErrorIs(t, err, ErrInvalidPrincipal)
}

func TestPrincipalContextCopiesMutableFields(t *testing.T) {
	p := Principal{Subject: "user-1", Scopes: []string{"orders:read"}, Claims: map[string]string{"email": "a@example.com"}}
	ctx := WithPrincipal(context.Background(), p)
	p.Scopes[0] = "mutated"
	p.Claims["email"] = "mutated"
	got, ok := FromContext(ctx)
	require.True(t, ok)
	assert.Equal(t, "orders:read", got.Scopes[0])
	assert.Equal(t, "a@example.com", got.Claims["email"])
	got.Scopes[0] = "handler-mutated"
	again, ok := FromContext(ctx)
	require.True(t, ok)
	assert.Equal(t, "orders:read", again.Scopes[0])
}
