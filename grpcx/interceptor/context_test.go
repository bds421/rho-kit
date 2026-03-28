package interceptor_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/bds421/rho-kit/grpcx/interceptor"
)

func TestUserID_EmptyWithoutAuth(t *testing.T) {
	assert.Equal(t, "", interceptor.UserID(context.Background()))
}

func TestUserPermissions_NilWithoutAuth(t *testing.T) {
	perms := interceptor.UserPermissions(context.Background())
	assert.Nil(t, perms)
}

func TestUserScopes_EmptyWithoutAuth(t *testing.T) {
	assert.Equal(t, "", interceptor.UserScopes(context.Background()))
}
