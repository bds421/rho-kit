package rules

import (
	"go/parser"
	"go/token"
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