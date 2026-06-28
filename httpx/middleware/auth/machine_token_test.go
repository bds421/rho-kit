package auth_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/httpx/v2/middleware/auth"
	"github.com/bds421/rho-kit/security/v2/session"
)

func TestSessionAuthenticator_AttemptsValidationForUsrPrefixedSessionShape(t *testing.T) {
	root := []byte("0123456789abcdef0123456789abcdef")
	signer, err := session.NewSigner(root, "session")
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	// One-dot session wire shape with usr_ prefix and extra underscores in the
	// payload segment. A generic three-underscore-segment heuristic would skip
	// this and break chains that rely on session auth before JWT.
	req.Header.Set("Authorization", "Bearer usr_aaa_bbb.ccc")

	_, err = auth.NewSessionAuthenticator(session.Validator{Signer: signer}).Authenticate(req)
	require.Error(t, err)
	require.True(t, errors.Is(err, auth.ErrInvalidCredentials),
		"session-shaped token must be validated, not skipped as machine")
	require.False(t, errors.Is(err, auth.ErrUnauthenticated))
}

func TestSessionAuthenticator_FallsThroughScopedKeyWireShape(t *testing.T) {
	root := []byte("0123456789abcdef0123456789abcdef")
	signer, err := session.NewSigner(root, "session")
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer rhosk_lookup_secretpart")

	_, err = auth.NewSessionAuthenticator(session.Validator{Signer: signer}).Authenticate(req)
	require.Error(t, err)
	require.True(t, errors.Is(err, auth.ErrUnauthenticated),
		"scoped key wire shape has no dot and must fall through to later strategies")
}