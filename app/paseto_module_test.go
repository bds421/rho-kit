package app

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/crypto/v2/paseto"
)

func mustPASETOProvider(t *testing.T) *paseto.Provider {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	p, err := paseto.NewProvider(context.Background(),
		func(_ context.Context) ([]ed25519.PublicKey, error) {
			return []ed25519.PublicKey{pub}, nil
		},
		time.Hour,
		paseto.WithVerifyOptions(
			paseto.WithExpectedIssuer("svc"),
			paseto.WithAllowAnyAudience(),
		),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = p.Close() })
	return p
}

func TestNewPasetoModule_PanicsOnNil(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on nil provider")
		}
	}()
	newPasetoModule(nil)
}

func TestWithPASETO_PanicsOnNil(t *testing.T) {
	b := New("test", "v1", BaseConfig{})
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on nil provider")
		}
	}()
	b.WithPASETO(nil)
}

func TestWithPASETO_PopulatesInfrastructure(t *testing.T) {
	p := mustPASETOProvider(t)
	m := newPasetoModule(p)
	infra := &Infrastructure{}
	m.Populate(infra)
	assert.Same(t, p, infra.PASETO)
}

func TestWithPASETO_RegistersOnBuilder(t *testing.T) {
	p := mustPASETOProvider(t)
	b := New("test", "v1", BaseConfig{}).WithPASETO(p)
	require.NotNil(t, b.pasetoProvider)
	assert.Same(t, p, b.pasetoProvider)
}
