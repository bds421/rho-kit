package rules

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAuthIdentityActorRule_FlagsUserIDOnlyLiteral(t *testing.T) {
	const src = `package svc
import "github.com/bds421/rho-kit/httpx/v2/middleware/auth"
func f() auth.Identity {
	return auth.Identity{UserID: "550e8400-e29b-41d4-a716-446655440000"}
}`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "svc.go", src, 0)
	require.NoError(t, err)
	findings := (authIdentityActorRule{}).Run(fset, file)
	require.Len(t, findings, 1)
	assert.Equal(t, "auth-identity-actor-drift", findings[0].Rule)
}

func TestAuthIdentityActorRule_AllowsSubjectActor(t *testing.T) {
	const src = `package svc
import "github.com/bds421/rho-kit/httpx/v2/middleware/auth"
func f() auth.Identity {
	return auth.Identity{Subject: "x", Actor: "y", ActorKind: auth.ActorAPIKey}
}`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "svc.go", src, 0)
	require.NoError(t, err)
	assert.Empty(t, (authIdentityActorRule{}).Run(fset, file))
}

func TestAuthIdentityActorRule_IgnoresLocalIdentityType(t *testing.T) {
	// Bare Identity{} is a local type when auth is imported under a name —
	// must not flag or offer a Fix that would corrupt the local struct.
	const src = `package svc
import auth "github.com/bds421/rho-kit/httpx/v2/middleware/auth"
type Identity struct{ UserID string }
func f() Identity {
	_ = auth.Identity{}
	return Identity{UserID: "local"}
}`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "svc.go", src, 0)
	require.NoError(t, err)
	assert.Empty(t, (authIdentityActorRule{}).Run(fset, file))
}

func TestFixAuthIdentityDrift_AddsSubjectActorKind(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "svc.go")
	const before = `package svc

import "github.com/bds421/rho-kit/httpx/v2/middleware/auth"

func f() auth.Identity {
	return auth.Identity{UserID: "550e8400-e29b-41d4-a716-446655440000"}
}
`
	require.NoError(t, os.WriteFile(path, []byte(before), 0o644))

	msg, err := fixAuthIdentityDrift(path, 6)
	require.NoError(t, err)
	assert.Contains(t, msg, "added Subject, Actor, and ActorKind")

	got, err := os.ReadFile(path)
	require.NoError(t, err)
	const want = `package svc

import "github.com/bds421/rho-kit/httpx/v2/middleware/auth"

func f() auth.Identity {
	return auth.Identity{Subject: "550e8400-e29b-41d4-a716-446655440000", Actor: "550e8400-e29b-41d4-a716-446655440000", ActorKind: auth.ActorUser, UserID: "550e8400-e29b-41d4-a716-446655440000"}
}
`
	assert.Equal(t, want, string(got))

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, got, 0)
	require.NoError(t, err)
	assert.Empty(t, (authIdentityActorRule{}).Run(fset, file))
}