package identity_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/security/v2/identity"
)

func TestApplyJWTActor_DefaultUser(t *testing.T) {
	subj, actor, kind, ok := identity.ApplyJWTActor(testUserID, nil, identity.JWTActorMapping{})
	require.True(t, ok)
	assert.Equal(t, testUserID, subj)
	assert.Equal(t, testUserID, actor)
	assert.Equal(t, identity.ActorUser, kind)
}

func TestApplyJWTActor_ServiceFromClientID(t *testing.T) {
	claims := map[string]string{"client_id": "payments-svc"}
	claim := func(name string) (string, bool) {
		v, ok := claims[name]
		return v, ok && v != ""
	}
	subj, actor, kind, ok := identity.ApplyJWTActor(testUserID, claim, identity.JWTActorMapping{
		ServiceActorClaim: "client_id",
	})
	require.True(t, ok)
	assert.Equal(t, testUserID, subj)
	assert.Equal(t, "payments-svc", actor)
	assert.Equal(t, identity.ActorService, kind)
}

func TestApplyJWTActor_ActorClaimOverride(t *testing.T) {
	claims := map[string]string{"act": "operator-1"}
	claim := func(name string) (string, bool) { return claims[name], claims[name] != "" }
	_, actor, kind, ok := identity.ApplyJWTActor(testUserID, claim, identity.JWTActorMapping{
		ActorClaim: "act",
	})
	require.True(t, ok)
	assert.Equal(t, "operator-1", actor)
	assert.Equal(t, identity.ActorUser, kind)
}