package rules

import (
	"go/parser"
	"go/token"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOIDCMemoryStoreRule_FlagsProductionStore(t *testing.T) {
	const src = `package svc
import oauth "github.com/bds421/rho-kit/auth/oauth2/v2"
func f() { _ = oauth.NewMemorySessionStore(); _ = oauth.NewMemoryStateStore() }`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "svc.go", src, parser.ParseComments)
	require.NoError(t, err)
	findings := (oidcMemoryStoreRule{}).Run(fset, file)
	require.Len(t, findings, 2)
	assert.Equal(t, "oidc-memory-store", findings[0].Rule)
	assert.Equal(t, High, findings[0].Severity)
}

func TestOIDCMemoryStoreRule_IgnoresTests(t *testing.T) {
	const src = `package svc
import "github.com/bds421/rho-kit/auth/oauth2/v2"
func f() { _ = oauth2.NewMemorySessionStore() }`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "svc_test.go", src, parser.ParseComments)
	require.NoError(t, err)
	assert.Empty(t, (oidcMemoryStoreRule{}).Run(fset, file))
}
