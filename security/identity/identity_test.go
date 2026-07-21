package identity_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/bds421/rho-kit/security/v2/identity"
)

const testUserID = "11111111-1111-1111-1111-111111111111"

func TestIsMachineKind(t *testing.T) {
	assert.False(t, identity.IsMachineKind(identity.ActorUser))
	assert.True(t, identity.IsMachineKind(identity.ActorAPIKey))
	assert.True(t, identity.IsMachineKind(identity.ActorOAuthClient))
	assert.True(t, identity.IsMachineKind(identity.ActorService))
}

func TestFormat(t *testing.T) {
	assert.Equal(t, "user:"+testUserID, identity.Format(identity.Ref{
		Subject: testUserID, Actor: testUserID, Kind: identity.ActorUser,
	}))
	assert.Equal(t, "api_key:key99", identity.Format(identity.Ref{
		Subject: testUserID, Actor: "key99", Kind: identity.ActorAPIKey,
	}))
	assert.Equal(t, "service:cn:backend", identity.Format(identity.Ref{
		Subject: testUserID, Actor: "cn:backend", Kind: identity.ActorService,
	}))
}
