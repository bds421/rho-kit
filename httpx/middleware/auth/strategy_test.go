package auth_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/httpx/v2/middleware/auth"
)

const testUserID = "11111111-2222-3333-4444-555555555555"

func newTerminal() (*http.Handler, *struct {
	called bool
	userID string
	perms  []string
	scopes string
	trust  bool
}) {
	state := &struct {
		called bool
		userID string
		perms  []string
		scopes string
		trust  bool
	}{}
	var h http.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		state.called = true
		state.userID = auth.UserID(r.Context())
		state.perms = auth.Permissions(r.Context())
		state.scopes = auth.Scopes(r.Context())
		state.trust = auth.IsTrustedS2S(r.Context())
		w.WriteHeader(http.StatusNoContent)
	})
	return &h, state
}

func TestStrategy_StampsIdentityOntoContext(t *testing.T) {
	a := auth.AuthenticatorFunc(func(*http.Request) (auth.Identity, error) {
		return auth.Identity{
			UserID:      testUserID,
			Permissions: []string{"a", "b"},
			Scopes:      "read write",
		}, nil
	})
	terminal, state := newTerminal()
	h := auth.Strategy(a)(*terminal)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	require.Equal(t, http.StatusNoContent, rec.Code, "successful auth must reach handler")
	require.True(t, state.called)
	assert.Equal(t, testUserID, state.userID)
	assert.Equal(t, []string{"a", "b"}, state.perms)
	assert.Equal(t, "read write", state.scopes)
	assert.False(t, state.trust, "Trusted=false must NOT set the S2S marker")
}

func TestStrategy_TrustedIdentityStampsS2SMarker(t *testing.T) {
	a := auth.AuthenticatorFunc(func(*http.Request) (auth.Identity, error) {
		return auth.Identity{UserID: testUserID, Trusted: true}, nil
	})
	terminal, state := newTerminal()
	h := auth.Strategy(a)(*terminal)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	require.True(t, state.trust, "Trusted=true must set the S2S marker so downstream RBAC accepts the call")
}

func TestStrategy_UnauthenticatedReturns401(t *testing.T) {
	a := auth.AuthenticatorFunc(func(*http.Request) (auth.Identity, error) {
		return auth.Identity{}, auth.ErrUnauthenticated
	})
	terminal, state := newTerminal()
	h := auth.Strategy(a)(*terminal)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	require.Equal(t, http.StatusUnauthorized, rec.Code)
	require.False(t, state.called, "handler must NOT run when strategy rejects the request")
}

func TestStrategy_InvalidCredentialsReturns401(t *testing.T) {
	a := auth.AuthenticatorFunc(func(*http.Request) (auth.Identity, error) {
		return auth.Identity{}, auth.ErrInvalidCredentials
	})
	terminal, _ := newTerminal()
	h := auth.Strategy(a)(*terminal)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	require.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestStrategy_NonUUIDSubjectRejected(t *testing.T) {
	a := auth.AuthenticatorFunc(func(*http.Request) (auth.Identity, error) {
		return auth.Identity{UserID: "not-a-uuid"}, nil
	})
	terminal, state := newTerminal()
	h := auth.Strategy(a)(*terminal)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	require.Equal(t, http.StatusUnauthorized, rec.Code,
		"non-UUID subject must be rejected even when the strategy reports success")
	require.False(t, state.called)
}

func TestStrategy_PanicInStrategyRecoveredAndReturns401(t *testing.T) {
	a := auth.AuthenticatorFunc(func(*http.Request) (auth.Identity, error) {
		panic("simulated bug")
	})
	terminal, state := newTerminal()
	h := auth.Strategy(a)(*terminal)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	require.Equal(t, http.StatusUnauthorized, rec.Code,
		"a panicking strategy must not crash the server; it must surface as 401")
	require.False(t, state.called)
}

func TestStrategy_NilAuthenticatorPanics(t *testing.T) {
	require.PanicsWithValue(t,
		"middleware/auth: Strategy requires a non-nil Authenticator",
		func() { auth.Strategy(nil) })
}

func TestChain_TriesEveryStrategyUntilOneAuthenticates(t *testing.T) {
	calls := []string{}
	first := auth.AuthenticatorFunc(func(*http.Request) (auth.Identity, error) {
		calls = append(calls, "first")
		return auth.Identity{}, auth.ErrUnauthenticated
	})
	second := auth.AuthenticatorFunc(func(*http.Request) (auth.Identity, error) {
		calls = append(calls, "second")
		return auth.Identity{UserID: testUserID}, nil
	})
	third := auth.AuthenticatorFunc(func(*http.Request) (auth.Identity, error) {
		calls = append(calls, "third")
		return auth.Identity{}, auth.ErrUnauthenticated
	})

	id, err := auth.Chain(first, second, third).Authenticate(httptest.NewRequest(http.MethodGet, "/", nil))
	require.NoError(t, err)
	require.Equal(t, testUserID, id.UserID)
	require.Equal(t, []string{"first", "second"}, calls,
		"chain must stop at the first successful strategy")
}

func TestChain_InvalidCredentialsStopsChain(t *testing.T) {
	// Defence-in-depth: a forged Bearer must not fall through to
	// API-key. The chain stops at the first non-Unauthenticated
	// error so the attacker cannot pivot to a weaker strategy.
	calls := []string{}
	first := auth.AuthenticatorFunc(func(*http.Request) (auth.Identity, error) {
		calls = append(calls, "first")
		return auth.Identity{}, auth.ErrInvalidCredentials
	})
	second := auth.AuthenticatorFunc(func(*http.Request) (auth.Identity, error) {
		calls = append(calls, "second")
		return auth.Identity{UserID: testUserID}, nil
	})

	_, err := auth.Chain(first, second).Authenticate(httptest.NewRequest(http.MethodGet, "/", nil))
	require.Error(t, err)
	require.True(t, errors.Is(err, auth.ErrInvalidCredentials),
		"chain must surface invalid-credentials and stop the chain")
	require.Equal(t, []string{"first"}, calls,
		"chain must NOT continue after a non-Unauthenticated error")
}

func TestChain_AllUnauthenticatedSurfacesUnauthenticated(t *testing.T) {
	first := auth.AuthenticatorFunc(func(*http.Request) (auth.Identity, error) {
		return auth.Identity{}, auth.ErrUnauthenticated
	})
	second := auth.AuthenticatorFunc(func(*http.Request) (auth.Identity, error) {
		return auth.Identity{}, auth.ErrUnauthenticated
	})

	_, err := auth.Chain(first, second).Authenticate(httptest.NewRequest(http.MethodGet, "/", nil))
	require.True(t, errors.Is(err, auth.ErrUnauthenticated))
}

func TestChain_EmptyPanics(t *testing.T) {
	require.PanicsWithValue(t,
		"middleware/auth: Chain requires at least one strategy",
		func() { auth.Chain() })
}

func TestChain_NilElementPanics(t *testing.T) {
	require.PanicsWithValue(t,
		"middleware/auth: Chain strategies must not be nil",
		func() {
			auth.Chain(auth.AuthenticatorFunc(func(*http.Request) (auth.Identity, error) {
				return auth.Identity{}, nil
			}), nil)
		})
}

func TestAPIKeyAuthenticator_HappyPath(t *testing.T) {
	v := auth.APIKeyVerifierFunc(func(_ context.Context, key string) (auth.Identity, error) {
		if key != "secret-key" {
			return auth.Identity{}, errors.New("nope")
		}
		return auth.Identity{UserID: testUserID, Permissions: []string{"a"}}, nil
	})
	a := auth.NewAPIKeyAuthenticator("X-API-Key", v)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-API-Key", "secret-key")
	id, err := a.Authenticate(req)
	require.NoError(t, err)
	require.Equal(t, testUserID, id.UserID)
	require.Equal(t, []string{"a"}, id.Permissions)
}

func TestAPIKeyAuthenticator_AbsentHeaderUnauthenticated(t *testing.T) {
	v := auth.APIKeyVerifierFunc(func(context.Context, string) (auth.Identity, error) {
		return auth.Identity{UserID: testUserID}, nil
	})
	a := auth.NewAPIKeyAuthenticator("X-API-Key", v)

	_, err := a.Authenticate(httptest.NewRequest(http.MethodGet, "/", nil))
	require.True(t, errors.Is(err, auth.ErrUnauthenticated),
		"absent header must be Unauthenticated so a Chain can fall through")
}

func TestAPIKeyAuthenticator_MultipleHeadersRejected(t *testing.T) {
	v := auth.APIKeyVerifierFunc(func(context.Context, string) (auth.Identity, error) {
		return auth.Identity{UserID: testUserID}, nil
	})
	a := auth.NewAPIKeyAuthenticator("X-API-Key", v)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Add("X-API-Key", "first")
	req.Header.Add("X-API-Key", "second")
	_, err := a.Authenticate(req)
	require.True(t, errors.Is(err, auth.ErrInvalidCredentials),
		"multiple header values must be invalid-credentials, not unauthenticated")
}

func TestAPIKeyAuthenticator_VerifierErrorIsInvalidCredentials(t *testing.T) {
	v := auth.APIKeyVerifierFunc(func(context.Context, string) (auth.Identity, error) {
		return auth.Identity{}, errors.New("nope")
	})
	a := auth.NewAPIKeyAuthenticator("X-API-Key", v)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-API-Key", "anything")
	_, err := a.Authenticate(req)
	require.True(t, errors.Is(err, auth.ErrInvalidCredentials))
}

func TestAPIKeyAuthenticator_VerifierErrorWrapsCause(t *testing.T) {
	// A verifier-internal failure (DB outage, context cancelled, timeout)
	// must still surface as ErrInvalidCredentials for callers and the
	// opaque wire response, but the underlying cause must be preserved so
	// operators can tell an infra outage apart from a forged key in logs.
	cause := errors.New("dial tcp: connection refused")
	v := auth.APIKeyVerifierFunc(func(context.Context, string) (auth.Identity, error) {
		return auth.Identity{}, cause
	})
	a := auth.NewAPIKeyAuthenticator("X-API-Key", v)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-API-Key", "anything")
	_, err := a.Authenticate(req)

	require.Error(t, err)
	require.True(t, errors.Is(err, auth.ErrInvalidCredentials),
		"verifier failure must remain ErrInvalidCredentials for callers")
	require.Contains(t, err.Error(), cause.Error(),
		"verifier error cause must be preserved for logging")
}

func TestAPIKeyAuthenticator_VerifierErrorStillStopsChain(t *testing.T) {
	// Defence-in-depth: preserving the cause must NOT let a verifier
	// failure fall through to a weaker strategy in a Chain, even if the
	// cause itself happens to wrap ErrUnauthenticated.
	calls := []string{}
	first := auth.NewAPIKeyAuthenticator("X-API-Key",
		auth.APIKeyVerifierFunc(func(context.Context, string) (auth.Identity, error) {
			calls = append(calls, "first")
			return auth.Identity{}, auth.ErrUnauthenticated
		}))
	second := auth.AuthenticatorFunc(func(*http.Request) (auth.Identity, error) {
		calls = append(calls, "second")
		return auth.Identity{UserID: testUserID}, nil
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-API-Key", "present-but-rejected")
	_, err := auth.Chain(first, second).Authenticate(req)

	require.Error(t, err)
	require.True(t, errors.Is(err, auth.ErrInvalidCredentials),
		"present-but-invalid key must surface as ErrInvalidCredentials")
	require.False(t, errors.Is(err, auth.ErrUnauthenticated),
		"a present key's verifier failure must not masquerade as Unauthenticated")
	require.Equal(t, []string{"first"}, calls,
		"chain must stop after the API-key verifier failed for a present key")
}

func TestAPIKeyAuthenticator_NilPanics(t *testing.T) {
	require.PanicsWithValue(t,
		"middleware/auth: NewAPIKeyAuthenticator requires a non-nil verifier",
		func() { auth.NewAPIKeyAuthenticator("X-API-Key", nil) })
}

func TestAPIKeyAuthenticator_EmptyHeaderNamePanics(t *testing.T) {
	require.PanicsWithValue(t,
		"middleware/auth: NewAPIKeyAuthenticator requires a non-empty header name",
		func() {
			auth.NewAPIKeyAuthenticator("", auth.APIKeyVerifierFunc(func(context.Context, string) (auth.Identity, error) {
				return auth.Identity{}, nil
			}))
		})
}

func TestAPIKeyAuthenticator_InvalidHeaderNamePanics(t *testing.T) {
	require.Panics(t, func() {
		auth.NewAPIKeyAuthenticator("X Api Key\n", auth.APIKeyVerifierFunc(func(context.Context, string) (auth.Identity, error) {
			return auth.Identity{}, nil
		}))
	})
}

func TestNewJWTAuthenticator_NilProviderPanics(t *testing.T) {
	require.PanicsWithValue(t,
		"middleware/auth: NewJWTAuthenticator requires a non-nil provider",
		func() { auth.NewJWTAuthenticator(nil) })
}
