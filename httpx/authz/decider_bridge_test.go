package authz_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	kitauthz "github.com/bds421/rho-kit/authz"
	httpxauthz "github.com/bds421/rho-kit/httpx/authz"
)

func TestFromDecider_NilPanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on nil Decider")
		}
	}()
	httpxauthz.FromDecider(nil)
}

func TestFromDecider_AllowMapsToTrue(t *testing.T) {
	m := kitauthz.NewMemory()
	m.Grant("alice", "read", "doc:1")
	p := httpxauthz.FromDecider(m)

	allowed, err := p.Allowed(context.Background(), "alice", "read", "doc:1")
	require.NoError(t, err)
	assert.True(t, allowed)
}

func TestFromDecider_DeniedMapsToFalseNilErr(t *testing.T) {
	m := kitauthz.NewMemory()
	p := httpxauthz.FromDecider(m)

	allowed, err := p.Allowed(context.Background(), "alice", "read", "doc:1")
	require.NoError(t, err, "ErrDenied must map to (false, nil), not propagate up")
	assert.False(t, allowed)
}

type erroringDecider struct{ err error }

func (e erroringDecider) Allow(_ context.Context, _, _, _ string) error { return e.err }

func TestFromDecider_EngineErrorSurfaces(t *testing.T) {
	want := errors.New("openfga: connection refused")
	p := httpxauthz.FromDecider(erroringDecider{err: want})

	allowed, err := p.Allowed(context.Background(), "alice", "read", "doc:1")
	assert.False(t, allowed)
	require.Error(t, err)
	assert.True(t, errors.Is(err, want), "engine errors must propagate, not be swallowed")
}
