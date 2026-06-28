package auth_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/httpx/v2/middleware/auth"
	"github.com/bds421/rho-kit/security/v2/apikey"
	"github.com/bds421/rho-kit/security/v2/jwtutil"
)

func TestIdentity_Normalize_PrefixedSubject(t *testing.T) {
	prefixed := jwtutil.SubjectPrefixUser + testUserID
	id := auth.Identity{Subject: prefixed}.Normalize()
	assert.Equal(t, testUserID, id.Subject)
	assert.Equal(t, testUserID, id.UserID)
}

func TestIdentity_Normalize_LegacyUserID(t *testing.T) {
	id := auth.Identity{UserID: testUserID, Tenant: "t1"}.Normalize()
	assert.Equal(t, testUserID, id.Subject)
	assert.Equal(t, testUserID, id.UserID)
	assert.Equal(t, testUserID, id.Actor)
	assert.Equal(t, auth.ActorUser, id.ActorKind)
}

func TestIdentityFromScopedKey_BoundUser(t *testing.T) {
	id := auth.IdentityFromScopedKey(apikey.Principal{
		UserID: testUserID,
		Tenant: "tenant-a",
		Role:   "member",
		Scopes: []string{"read:contacts"},
		Kind:   apikey.ScopedKindAPIKey,
		KeyID:  "Ab12Cd34",
	})
	assert.Equal(t, testUserID, id.Subject)
	assert.Equal(t, "Ab12Cd34", id.Actor)
	assert.Equal(t, auth.ActorAPIKey, id.ActorKind)
	assert.Equal(t, []string{"read:contacts"}, id.ScopeList)
	assert.Equal(t, "read:contacts", id.Scopes)
	assert.Equal(t, []string{"read:contacts"}, id.Permissions)
}

func TestIdentityFromScopedKey_OAuthClient(t *testing.T) {
	id := auth.IdentityFromScopedKey(apikey.Principal{
		UserID: testUserID,
		Tenant: "tenant-a",
		Kind:   apikey.ScopedKindOAuthClient,
		KeyID:  "client1",
	})
	assert.Equal(t, auth.ActorOAuthClient, id.ActorKind)
}

func TestIdentityFromScopedKey_UnboundMachine(t *testing.T) {
	id := auth.IdentityFromScopedKey(apikey.Principal{
		Tenant: "tenant-a",
		Kind:   apikey.ScopedKindAPIKey,
		KeyID:  "lookup8c",
	})
	assert.Empty(t, id.Subject)
	assert.Equal(t, "lookup8c", id.Actor)
	assert.Equal(t, auth.ActorAPIKey, id.ActorKind)
}

func TestFormatActor(t *testing.T) {
	assert.Equal(t, "user:"+testUserID, auth.FormatActor(auth.Identity{
		Subject: testUserID, Actor: testUserID, ActorKind: auth.ActorUser,
	}))
	assert.Equal(t, "api_key:key99", auth.FormatActor(auth.Identity{
		Subject: testUserID, Actor: "key99", ActorKind: auth.ActorAPIKey,
	}))
}

func TestStrategy_IdentityMatrix(t *testing.T) {
	cases := []struct {
		name       string
		id         auth.Identity
		wantCode   int
		wantSubj   string
		wantActor  string
		wantKind   auth.ActorKind
		wantMachine bool
	}{
		{
			name: "human session",
			id: auth.Identity{
				Subject: testUserID, Actor: testUserID, ActorKind: auth.ActorUser,
				Tenant: "t1",
			},
			wantCode: http.StatusNoContent, wantSubj: testUserID, wantActor: testUserID,
			wantKind: auth.ActorUser,
		},
		{
			name: "scoped key bound",
			id: auth.Identity{
				Subject: testUserID, Actor: "keyid1", ActorKind: auth.ActorAPIKey,
				Tenant: "t1", ScopeList: []string{"read"},
			},
			wantCode: http.StatusNoContent, wantSubj: testUserID, wantActor: "keyid1",
			wantKind: auth.ActorAPIKey, wantMachine: true,
		},
		{
			name: "scoped key unbound",
			id: auth.Identity{
				Actor: "keyid2", ActorKind: auth.ActorAPIKey, Tenant: "t1",
			},
			wantCode: http.StatusNoContent, wantActor: "keyid2",
			wantKind: auth.ActorAPIKey, wantMachine: true,
		},
		{
			name: "oauth client",
			id: auth.Identity{
				Subject: testUserID, Actor: "oauth1", ActorKind: auth.ActorOAuthClient,
				Tenant: "t1",
			},
			wantCode: http.StatusNoContent, wantSubj: testUserID, wantActor: "oauth1",
			wantKind: auth.ActorOAuthClient, wantMachine: true,
		},
		{
			name: "non-uuid subject",
			id: auth.Identity{UserID: "not-a-uuid", Actor: "not-a-uuid", ActorKind: auth.ActorUser},
			wantCode: http.StatusUnauthorized,
		},
		{
			name: "unbound machine missing tenant",
			id: auth.Identity{Actor: "keyid3", ActorKind: auth.ActorAPIKey},
			wantCode: http.StatusUnauthorized,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotSubj, gotActor string
			var gotKind auth.ActorKind
			var gotMachine bool
			h := auth.Strategy(auth.AuthenticatorFunc(func(*http.Request) (auth.Identity, error) {
				return tc.id, nil
			}))(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotSubj = auth.Subject(r.Context())
				gotActor = auth.Actor(r.Context())
				gotKind = auth.ActorKindFromContext(r.Context())
				gotMachine = auth.IsMachine(r.Context())
				w.WriteHeader(http.StatusNoContent)
			}))
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
			require.Equal(t, tc.wantCode, rec.Code)
			if tc.wantCode != http.StatusNoContent {
				return
			}
			assert.Equal(t, tc.wantSubj, gotSubj)
			assert.Equal(t, tc.wantActor, gotActor)
			assert.Equal(t, tc.wantKind, gotKind)
			assert.Equal(t, tc.wantMachine, gotMachine)
		})
	}
}