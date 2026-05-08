package authz_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/authz/v2"
)

func TestMemory_GrantThenAllow(t *testing.T) {
	m := authz.NewMemory()
	m.Grant("alice", "read", "doc:1")
	require.NoError(t, m.Allow(context.Background(), "alice", "read", "doc:1"))
}

func TestMemory_DeniedByDefault(t *testing.T) {
	m := authz.NewMemory()
	err := m.Allow(context.Background(), "alice", "read", "doc:1")
	require.Error(t, err)
	assert.True(t, errors.Is(err, authz.ErrDenied))
}

func TestMemory_RevokeRemovesGrant(t *testing.T) {
	m := authz.NewMemory()
	m.Grant("alice", "read", "doc:1")
	m.Revoke("alice", "read", "doc:1")
	err := m.Allow(context.Background(), "alice", "read", "doc:1")
	assert.True(t, errors.Is(err, authz.ErrDenied))
}

func TestMemory_DistinctTuplesIsolated(t *testing.T) {
	m := authz.NewMemory()
	m.Grant("alice", "read", "doc:1")
	// Same subject, different resource → denied.
	assert.True(t, errors.Is(m.Allow(context.Background(), "alice", "read", "doc:2"), authz.ErrDenied))
	// Different subject, same resource → denied.
	assert.True(t, errors.Is(m.Allow(context.Background(), "bob", "read", "doc:1"), authz.ErrDenied))
}

func TestAllow_DispatchesToDecider(t *testing.T) {
	m := authz.NewMemory()
	m.Grant("alice", "write", "doc:1")
	require.NoError(t, authz.Allow(context.Background(), m, authz.Request{
		Subject: "alice", Action: "write", Resource: "doc:1",
	}))
}

// Compile-time check: Memory satisfies Decider.
var _ authz.Decider = (*authz.Memory)(nil)
