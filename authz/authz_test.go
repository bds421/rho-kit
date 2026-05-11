package authz_test

import (
	"context"
	"errors"
	"strings"
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

func TestMemory_PanicsOnInvalidGrant(t *testing.T) {
	m := authz.NewMemory()
	require.Panics(t, func() { m.Grant("alice smith", "read", "doc:1") })
}

func TestMemory_InvalidAllowFailsClosed(t *testing.T) {
	m := authz.NewMemory()
	err := m.Allow(context.Background(), "alice", "read", "bad resource")
	require.Error(t, err)
	assert.ErrorIs(t, err, authz.ErrInvalidRequest)
	assert.ErrorIs(t, err, authz.ErrDenied)
}

func TestMemory_NilReceiverReturnsErrNoDecider(t *testing.T) {
	var m *authz.Memory
	err := m.Allow(context.Background(), "alice", "read", "doc:1")
	require.Error(t, err)
	assert.ErrorIs(t, err, authz.ErrNoDecider)
}

func TestAllow_DispatchesToDecider(t *testing.T) {
	m := authz.NewMemory()
	m.Grant("alice", "write", "doc:1")
	require.NoError(t, authz.Allow(context.Background(), m, authz.Request{
		Subject: "alice", Action: "write", Resource: "doc:1",
	}))
}

func TestAllow_NilDeciderReturnsErrNoDecider(t *testing.T) {
	err := authz.Allow(context.Background(), nil, authz.Request{
		Subject: "alice", Action: "read", Resource: "doc:1",
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, authz.ErrNoDecider))
}

func TestAllow_NilContextReturnsErrInvalidContext(t *testing.T) {
	m := authz.NewMemory()
	var ctx context.Context
	err := authz.Allow(ctx, m, authz.Request{
		Subject: "alice", Action: "read", Resource: "doc:1",
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, authz.ErrInvalidContext)
}

type recordingDecider struct {
	called bool
}

func (d *recordingDecider) Allow(context.Context, string, string, string) error {
	d.called = true
	return nil
}

func TestAllow_RejectsInvalidRequestBeforeDecider(t *testing.T) {
	d := &recordingDecider{}
	err := authz.Allow(context.Background(), d, authz.Request{
		Subject: "alice", Action: "read", Resource: "bad resource",
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, authz.ErrInvalidRequest)
	assert.ErrorIs(t, err, authz.ErrDenied)
	assert.False(t, d.called)
}

type panicDecider struct{}

func (panicDecider) Allow(context.Context, string, string, string) error {
	panic("engine invariant failed")
}

func TestAllow_DeciderPanicReturnsErrDeciderPanic(t *testing.T) {
	err := authz.Allow(context.Background(), panicDecider{}, authz.Request{
		Subject: "alice", Action: "read", Resource: "doc:1",
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, authz.ErrDeciderPanic))
	assert.Contains(t, err.Error(), "<redacted panic value: string>")
	assert.NotContains(t, err.Error(), "engine invariant failed")
}

func TestValidateRequest_RejectsMalformedParts(t *testing.T) {
	for _, tc := range []struct {
		name string
		req  authz.Request
	}{
		{name: "empty-subject", req: authz.Request{Subject: "", Action: "read", Resource: "doc:1"}},
		{name: "action-space", req: authz.Request{Subject: "alice", Action: "read all", Resource: "doc:1"}},
		{name: "resource-control", req: authz.Request{Subject: "alice", Action: "read", Resource: "doc:\n1"}},
		{name: "subject-invalid-utf8", req: authz.Request{Subject: string([]byte{0xff}), Action: "read", Resource: "doc:1"}},
		{name: "resource-too-long", req: authz.Request{Subject: "alice", Action: "read", Resource: strings.Repeat("a", authz.MaxRequestPartLen+1)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := authz.ValidateRequest(tc.req)
			require.Error(t, err)
			assert.ErrorIs(t, err, authz.ErrInvalidRequest)
			assert.ErrorIs(t, err, authz.ErrDenied)
			if tc.name == "resource-too-long" {
				assert.NotContains(t, err.Error(), "512")
				assert.NotContains(t, err.Error(), "513")
			}
		})
	}
}

// Compile-time check: Memory satisfies Decider.
var _ authz.Decider = (*authz.Memory)(nil)
