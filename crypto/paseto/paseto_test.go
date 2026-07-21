package paseto

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func mustEd25519Pair(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	return pub, priv
}

// v4PublicPair is a test-only helper that builds both a verifier and a
// signer with matching configs. The split V4PublicSigner/V4PublicVerifier
// API lets the verifier-only services hold just public keys; for tests
// that round-trip we want both, so the helper hides the boilerplate.
type v4PublicPair struct {
	*V4PublicVerifier
	signer *V4PublicSigner
}

func (p *v4PublicPair) Sign(claims Claims, _ ed25519.PrivateKey) (string, error) {
	return p.signer.Sign(claims)
}

func newV4PublicPair(t *testing.T, pub ed25519.PublicKey, priv ed25519.PrivateKey, opts ...Option) (*v4PublicPair, error) {
	t.Helper()
	verifier, err := NewV4PublicVerifier([]ed25519.PublicKey{pub}, opts...)
	if err != nil {
		return nil, err
	}
	var signer *V4PublicSigner
	if priv != nil {
		signer, err = NewV4PublicSigner(priv, opts...)
		if err != nil {
			return nil, err
		}
	}
	return &v4PublicPair{V4PublicVerifier: verifier, signer: signer}, nil
}

func randomV4LocalKey(t *testing.T) []byte {
	t.Helper()
	key := make([]byte, 32)
	_, err := rand.Read(key)
	require.NoError(t, err)
	return key
}

func futureExp() time.Time { return time.Now().Add(time.Hour) }

func TestV4Public_RoundTrip(t *testing.T) {
	pub, priv := mustEd25519Pair(t)

	v, err := newV4PublicPair(t, pub, priv,
		WithExpectedIssuer("svc-A"),
		WithExpectedAudience("svc-B"),
	)
	require.NoError(t, err)

	now := time.Now().UTC().Truncate(time.Second)
	tok, err := v.Sign(Claims{
		Subject:   "user-1",
		Issuer:    "svc-A",
		Audience:  []string{"svc-B"},
		IssuedAt:  now,
		ExpiresAt: now.Add(time.Hour),
	}, priv)
	require.NoError(t, err)

	claims, err := v.Verify(tok, now)
	require.NoError(t, err)
	assert.Equal(t, "user-1", claims.Subject)
	assert.Equal(t, "svc-A", claims.Issuer)
	assert.Equal(t, []string{"svc-B"}, claims.Audience)
}

func TestV4Public_RejectsExpired(t *testing.T) {
	pub, priv := mustEd25519Pair(t)
	v, err := newV4PublicPair(t, pub, priv,
		WithExpectedIssuer("svc-A"),
		WithExpectedAudience("svc-B"),
	)
	require.NoError(t, err)

	now := time.Now()
	tok, err := v.Sign(Claims{
		Issuer:    "svc-A",
		Audience:  []string{"svc-B"},
		ExpiresAt: now.Add(-time.Minute),
	}, priv)
	require.NoError(t, err)

	_, err = v.Verify(tok, now)
	assert.ErrorIs(t, err, ErrTokenExpired)
}

func TestV4Public_ZeroVerifyTimeUsesWallClock(t *testing.T) {
	pub, priv := mustEd25519Pair(t)
	v, err := newV4PublicPair(t, pub, priv,
		WithExpectedIssuer("svc-A"),
		WithExpectedAudience("svc-B"),
	)
	require.NoError(t, err)

	tok, err := v.Sign(Claims{
		Issuer:    "svc-A",
		Audience:  []string{"svc-B"},
		ExpiresAt: time.Now().Add(-5 * time.Minute),
	}, priv)
	require.NoError(t, err)

	_, err = v.Verify(tok, time.Time{})
	assert.ErrorIs(t, err, ErrTokenExpired)
}

func TestV4Public_RejectsNotYetValid(t *testing.T) {
	pub, priv := mustEd25519Pair(t)
	v, err := newV4PublicPair(t, pub, priv,
		WithExpectedIssuer("svc-A"),
		WithExpectedAudience("svc-B"),
	)
	require.NoError(t, err)

	now := time.Now()
	tok, err := v.Sign(Claims{
		Issuer:    "svc-A",
		Audience:  []string{"svc-B"},
		NotBefore: now.Add(time.Minute),
		ExpiresAt: futureExp(),
	}, priv)
	require.NoError(t, err)

	_, err = v.Verify(tok, now)
	assert.ErrorIs(t, err, ErrTokenNotYet)
}

func TestV4Public_RejectsIssuerMismatch(t *testing.T) {
	pub, priv := mustEd25519Pair(t)

	signer, err := newV4PublicPair(t, pub, priv,
		WithAllowAnyIssuer(),
		WithExpectedAudience("svc-B"),
	)
	require.NoError(t, err)

	tok, err := signer.Sign(Claims{
		Issuer:    "wrong-secret-token-issuer",
		Audience:  []string{"svc-B"},
		ExpiresAt: futureExp(),
	}, priv)
	require.NoError(t, err)

	verifier, err := newV4PublicPair(t, pub, priv,
		WithExpectedIssuer("svc-A"),
		WithExpectedAudience("svc-B"),
	)
	require.NoError(t, err)

	_, err = verifier.Verify(tok, time.Now())
	assert.ErrorIs(t, err, ErrIssuerMismatch)
	assert.NotContains(t, err.Error(), "wrong-secret-token-issuer")
	assert.NotContains(t, err.Error(), "svc-A")
}

func TestV4Public_RejectsAudienceMismatch(t *testing.T) {
	pub, priv := mustEd25519Pair(t)

	signer, err := newV4PublicPair(t, pub, priv,
		WithExpectedIssuer("svc-A"),
		WithAllowAnyAudience(),
	)
	require.NoError(t, err)

	tok, err := signer.Sign(Claims{
		Issuer:    "svc-A",
		Audience:  []string{"wrong-secret-token-audience"},
		ExpiresAt: futureExp(),
	}, priv)
	require.NoError(t, err)

	verifier, err := newV4PublicPair(t, pub, priv,
		WithExpectedIssuer("svc-A"),
		WithExpectedAudience("svc-B"),
	)
	require.NoError(t, err)

	_, err = verifier.Verify(tok, time.Now())
	assert.ErrorIs(t, err, ErrAudienceUnknown)
	assert.NotContains(t, err.Error(), "wrong-secret-token-audience")
	assert.NotContains(t, err.Error(), "svc-B")
}

func TestV4Public_RejectsTamperedToken(t *testing.T) {
	pub, priv := mustEd25519Pair(t)
	v, err := newV4PublicPair(t, pub, priv,
		WithExpectedIssuer("svc-A"),
		WithExpectedAudience("svc-B"),
	)
	require.NoError(t, err)

	tok, err := v.Sign(Claims{
		Issuer:    "svc-A",
		Audience:  []string{"svc-B"},
		ExpiresAt: futureExp(),
	}, priv)
	require.NoError(t, err)

	tampered := tok[:len(tok)-1] + "X"
	_, err = v.Verify(tampered, time.Now())
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrTokenInvalid))
}

func TestVerify_InvalidTokenErrorsDoNotReflectTokenMaterial(t *testing.T) {
	pub, _ := mustEd25519Pair(t)
	publicVerifier, err := NewV4PublicVerifier(
		[]ed25519.PublicKey{pub},
		WithExpectedIssuer("svc-A"),
		WithExpectedAudience("svc-B"),
	)
	require.NoError(t, err)

	_, err = publicVerifier.Verify("v4.public.secret-token", time.Now())
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrTokenInvalid)
	assert.NotContains(t, err.Error(), "secret-token")

	localVerifier, err := NewV4Local(randomV4LocalKey(t),
		WithExpectedIssuer("svc-A"),
		WithExpectedAudience("svc-B"),
	)
	require.NoError(t, err)

	_, err = localVerifier.Verify("v4.local.secret-token", time.Now())
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrTokenInvalid)
	assert.NotContains(t, err.Error(), "secret-token")
}

func TestV4Public_RejectsKeyMismatch(t *testing.T) {
	pubA, _ := mustEd25519Pair(t)
	_, privB := mustEd25519Pair(t)

	verifier, err := NewV4PublicVerifier(
		[]ed25519.PublicKey{pubA},
		WithExpectedIssuer("svc-A"),
		WithExpectedAudience("svc-B"),
	)
	require.NoError(t, err)
	signer, err := NewV4PublicSigner(privB,
		WithExpectedIssuer("svc-A"),
		WithExpectedAudience("svc-B"),
	)
	require.NoError(t, err)

	tok, err := signer.Sign(Claims{
		Issuer:    "svc-A",
		Audience:  []string{"svc-B"},
		ExpiresAt: futureExp(),
	})
	require.NoError(t, err)

	_, err = verifier.Verify(tok, time.Now())
	assert.ErrorIs(t, err, ErrTokenInvalid)
}

func TestV4Public_AllowAnyIssuer(t *testing.T) {
	pub, priv := mustEd25519Pair(t)
	v, err := newV4PublicPair(t, pub, priv,
		WithAllowAnyIssuer(),
		WithExpectedAudience("svc-B"),
	)
	require.NoError(t, err)

	tok, err := v.Sign(Claims{
		Issuer:    "any-issuer",
		Audience:  []string{"svc-B"},
		ExpiresAt: futureExp(),
	}, priv)
	require.NoError(t, err)

	c, err := v.Verify(tok, time.Now())
	require.NoError(t, err)
	assert.Equal(t, "any-issuer", c.Issuer)
}

func TestV4Local_RoundTrip(t *testing.T) {
	key := randomV4LocalKey(t)
	v, err := NewV4Local(key,
		WithExpectedIssuer("svc-A"),
		WithExpectedAudience("svc-B"),
	)
	require.NoError(t, err)

	now := time.Now().UTC().Truncate(time.Second)
	tok, err := v.Seal(Claims{
		Subject:   "user-1",
		Issuer:    "svc-A",
		Audience:  []string{"svc-B"},
		ExpiresAt: now.Add(time.Hour),
	})
	require.NoError(t, err)

	claims, err := v.Verify(tok, now)
	require.NoError(t, err)
	assert.Equal(t, "user-1", claims.Subject)
}

func TestV4Local_RejectsKeyMismatch(t *testing.T) {
	keyA := randomV4LocalKey(t)
	keyB := randomV4LocalKey(t)

	issuer := WithExpectedIssuer("svc-A")
	audience := WithExpectedAudience("svc-B")

	vA, err := NewV4Local(keyA, issuer, audience)
	require.NoError(t, err)
	vB, err := NewV4Local(keyB, issuer, audience)
	require.NoError(t, err)

	tok, err := vA.Seal(Claims{
		Issuer:    "svc-A",
		Audience:  []string{"svc-B"},
		ExpiresAt: futureExp(),
	})
	require.NoError(t, err)

	_, err = vB.Verify(tok, time.Now())
	assert.ErrorIs(t, err, ErrTokenInvalid)
}

func TestNewV4Public_RequiresIssuerOrAllowAny(t *testing.T) {
	pub, _ := mustEd25519Pair(t)
	_, err := NewV4PublicVerifier(
		[]ed25519.PublicKey{pub},
		WithExpectedAudience("svc-B"),
	)
	assert.Error(t, err)
}

func TestNewV4Public_RequiresAudienceOrAllowAny(t *testing.T) {
	pub, _ := mustEd25519Pair(t)
	_, err := NewV4PublicVerifier(
		[]ed25519.PublicKey{pub},
		WithExpectedIssuer("svc-A"),
	)
	assert.Error(t, err)
}

func TestNewV4Local_RejectsBadKeyLength(t *testing.T) {
	_, err := NewV4Local(make([]byte, 16),
		WithExpectedIssuer("svc-A"),
		WithExpectedAudience("svc-B"),
	)
	assert.Error(t, err)
	assert.NotContains(t, err.Error(), "16")
}

func TestClockSkew_WithinTolerance(t *testing.T) {
	pub, priv := mustEd25519Pair(t)
	v, err := newV4PublicPair(t, pub, priv,
		WithExpectedIssuer("svc-A"),
		WithExpectedAudience("svc-B"),
		WithClockSkewTolerance(30*time.Second),
	)
	require.NoError(t, err)

	now := time.Now()
	tok, err := v.Sign(Claims{
		Issuer:    "svc-A",
		Audience:  []string{"svc-B"},
		ExpiresAt: now.Add(-10 * time.Second),
	}, priv)
	require.NoError(t, err)

	_, err = v.Verify(tok, now)
	require.NoError(t, err)

	_, err = v.Verify(tok, now.Add(time.Minute))
	assert.ErrorIs(t, err, ErrTokenExpired)
}

func TestSign_RejectsMissingExpByDefault(t *testing.T) {
	pub, priv := mustEd25519Pair(t)
	v, err := newV4PublicPair(t, pub, priv,
		WithExpectedIssuer("svc-A"),
		WithExpectedAudience("svc-B"),
	)
	require.NoError(t, err)

	_, err = v.Sign(Claims{Issuer: "svc-A", Audience: []string{"svc-B"}}, priv)
	assert.ErrorIs(t, err, ErrNoExpiration)
}

func TestSeal_RejectsMissingExpByDefault(t *testing.T) {
	key := randomV4LocalKey(t)
	v, err := NewV4Local(key,
		WithExpectedIssuer("svc-A"),
		WithExpectedAudience("svc-B"),
	)
	require.NoError(t, err)

	_, err = v.Seal(Claims{Issuer: "svc-A", Audience: []string{"svc-B"}})
	assert.ErrorIs(t, err, ErrNoExpiration)
}

func TestSign_DefaultLifetimeApplied(t *testing.T) {
	pub, priv := mustEd25519Pair(t)
	v, err := newV4PublicPair(t, pub, priv,
		WithExpectedIssuer("svc-A"),
		WithExpectedAudience("svc-B"),
		WithDefaultLifetime(15*time.Minute),
	)
	require.NoError(t, err)

	tok, err := v.Sign(Claims{Issuer: "svc-A", Audience: []string{"svc-B"}}, priv)
	require.NoError(t, err)

	claims, err := v.Verify(tok, time.Now())
	require.NoError(t, err)
	assert.False(t, claims.ExpiresAt.IsZero())
	assert.WithinDuration(t, time.Now().Add(15*time.Minute), claims.ExpiresAt, 5*time.Second)
}

func TestOptions_RejectInvalidInput(t *testing.T) {
	pub, _ := mustEd25519Pair(t)
	assert.Panics(t, func() { WithDefaultLifetime(0) })
	assert.Panics(t, func() { WithDefaultLifetime(-time.Second) })

	// Nil opt panics — wiring bug surfaces at construction (matches the
	// canonical Option-shape contract used elsewhere in the kit).
	assert.Panics(t, func() {
		_, _ = NewV4PublicVerifier([]ed25519.PublicKey{pub}, nil)
	})
}

func TestVerify_RejectsTokenWithoutExp(t *testing.T) {
	pub, priv := mustEd25519Pair(t)
	signer, err := newV4PublicPair(t, pub, priv,
		WithExpectedIssuer("svc-A"),
		WithExpectedAudience("svc-B"),
		WithoutExpiration(),
	)
	require.NoError(t, err)

	tok, err := signer.Sign(Claims{Issuer: "svc-A", Audience: []string{"svc-B"}}, priv)
	require.NoError(t, err)

	verifier, err := newV4PublicPair(t, pub, priv,
		WithExpectedIssuer("svc-A"),
		WithExpectedAudience("svc-B"),
	)
	require.NoError(t, err)

	_, err = verifier.Verify(tok, time.Now())
	assert.ErrorIs(t, err, ErrTokenNoExp)
}

func TestVerify_AcceptsTokenWithoutExpWhenOptedOut(t *testing.T) {
	pub, priv := mustEd25519Pair(t)
	v, err := newV4PublicPair(t, pub, priv,
		WithExpectedIssuer("svc-A"),
		WithExpectedAudience("svc-B"),
		WithoutExpiration(),
	)
	require.NoError(t, err)

	tok, err := v.Sign(Claims{Issuer: "svc-A", Audience: []string{"svc-B"}}, priv)
	require.NoError(t, err)

	claims, err := v.Verify(tok, time.Now())
	require.NoError(t, err)
	assert.True(t, claims.ExpiresAt.IsZero())
}

func TestVerify_AcceptsFutureExp(t *testing.T) {
	pub, priv := mustEd25519Pair(t)
	v, err := newV4PublicPair(t, pub, priv,
		WithExpectedIssuer("svc-A"),
		WithExpectedAudience("svc-B"),
	)
	require.NoError(t, err)

	tok, err := v.Sign(Claims{
		Issuer:    "svc-A",
		Audience:  []string{"svc-B"},
		ExpiresAt: time.Now().Add(time.Hour),
	}, priv)
	require.NoError(t, err)

	_, err = v.Verify(tok, time.Now())
	assert.NoError(t, err)
}

func TestSign_RejectsMultipleAudiences(t *testing.T) {
	pub, priv := mustEd25519Pair(t)
	v, err := newV4PublicPair(t, pub, priv,
		WithExpectedIssuer("svc-A"),
		WithExpectedAudience("svc-B"),
	)
	require.NoError(t, err)

	_, err = v.Sign(Claims{
		Issuer:    "svc-A",
		Audience:  []string{"svc-B", "svc-C"},
		ExpiresAt: futureExp(),
	}, priv)
	assert.ErrorIs(t, err, ErrMultiAudience)
}

func TestVerify_AudIsSingleSourceOfTruth(t *testing.T) {
	pub, priv := mustEd25519Pair(t)
	v, err := newV4PublicPair(t, pub, priv,
		WithExpectedIssuer("svc-A"),
		WithExpectedAudience("svc-B"),
	)
	require.NoError(t, err)

	tok, err := v.Sign(Claims{
		Issuer:    "svc-A",
		Audience:  []string{"svc-B"},
		ExpiresAt: futureExp(),
	}, priv)
	require.NoError(t, err)

	claims, err := v.Verify(tok, time.Now())
	require.NoError(t, err)
	assert.Equal(t, []string{"svc-B"}, claims.Audience)
}

func TestSign_RejectsReservedClaimInCustom(t *testing.T) {
	pub, priv := mustEd25519Pair(t)
	v, err := newV4PublicPair(t, pub, priv,
		WithExpectedIssuer("svc-A"),
		WithExpectedAudience("svc-B"),
	)
	require.NoError(t, err)

	for _, name := range []string{"iss", "aud", "exp", "nbf", "iat", "sub", "jti", "kid", "aud_alt"} {
		t.Run(name, func(t *testing.T) {
			_, err := v.Sign(Claims{
				Issuer:    "svc-A",
				Audience:  []string{"svc-B"},
				ExpiresAt: futureExp(),
				Custom:    map[string]any{name: "x"},
			}, priv)
			assert.ErrorIs(t, err, ErrReservedClaim)
			assert.NotContains(t, err.Error(), name)
		})
	}
}

func TestSeal_RejectsReservedClaimInCustom(t *testing.T) {
	key := randomV4LocalKey(t)
	v, err := NewV4Local(key,
		WithExpectedIssuer("svc-A"),
		WithExpectedAudience("svc-B"),
	)
	require.NoError(t, err)

	_, err = v.Seal(Claims{
		Issuer:    "svc-A",
		Audience:  []string{"svc-B"},
		ExpiresAt: futureExp(),
		Custom:    map[string]any{"exp": "tomorrow"},
	})
	assert.ErrorIs(t, err, ErrReservedClaim)
	assert.NotContains(t, err.Error(), "exp")
}

func TestSign_AllowsCustomClaimsThatAreNotReserved(t *testing.T) {
	pub, priv := mustEd25519Pair(t)
	v, err := newV4PublicPair(t, pub, priv,
		WithExpectedIssuer("svc-A"),
		WithExpectedAudience("svc-B"),
	)
	require.NoError(t, err)

	tok, err := v.Sign(Claims{
		Issuer:    "svc-A",
		Audience:  []string{"svc-B"},
		ExpiresAt: futureExp(),
		Custom:    map[string]any{"role": "admin", "tenant": "t1"},
	}, priv)
	require.NoError(t, err)

	_, err = v.Verify(tok, time.Now())
	assert.NoError(t, err)
}

func TestVerify_PreservesCustomClaims(t *testing.T) {
	pub, priv := mustEd25519Pair(t)
	v, err := newV4PublicPair(t, pub, priv,
		WithExpectedIssuer("svc-A"),
		WithExpectedAudience("svc-B"),
	)
	require.NoError(t, err)

	tok, err := v.Sign(Claims{
		Issuer:    "svc-A",
		Audience:  []string{"svc-B"},
		ExpiresAt: futureExp(),
		Custom:    map[string]any{"role": "admin", "tenant": "x"},
	}, priv)
	require.NoError(t, err)

	claims, err := v.Verify(tok, time.Now())
	require.NoError(t, err)
	assert.Equal(t, "admin", claims.Custom["role"])
	assert.Equal(t, "x", claims.Custom["tenant"])
	for _, reserved := range []string{"iss", "aud", "exp", "nbf", "iat", "sub", "jti"} {
		_, present := claims.Custom[reserved]
		assert.False(t, present, "reserved claim %q must not appear in Custom", reserved)
	}
}

func TestVerify_PreservesCustomClaims_V4Local(t *testing.T) {
	key := randomV4LocalKey(t)
	v, err := NewV4Local(key,
		WithExpectedIssuer("svc-A"),
		WithExpectedAudience("svc-B"),
	)
	require.NoError(t, err)

	tok, err := v.Seal(Claims{
		Issuer:    "svc-A",
		Audience:  []string{"svc-B"},
		ExpiresAt: futureExp(),
		Custom:    map[string]any{"scope": "read", "tier": float64(2)},
	})
	require.NoError(t, err)

	claims, err := v.Verify(tok, time.Now())
	require.NoError(t, err)
	assert.Equal(t, "read", claims.Custom["scope"])
	assert.Equal(t, float64(2), claims.Custom["tier"])
}

func TestSign_RejectsCallerIssuerMismatch(t *testing.T) {
	pub, priv := mustEd25519Pair(t)
	v, err := newV4PublicPair(t, pub, priv,
		WithExpectedIssuer("svc-A"),
		WithExpectedAudience("svc-B"),
	)
	require.NoError(t, err)

	_, err = v.Sign(Claims{
		Issuer:    "wrong-secret-token-issuer",
		Audience:  []string{"svc-B"},
		ExpiresAt: futureExp(),
	}, priv)
	assert.ErrorIs(t, err, ErrIssuerMismatch)
	assert.NotContains(t, err.Error(), "wrong-secret-token-issuer")
	assert.NotContains(t, err.Error(), "svc-A")
}

func TestSign_StampsConfiguredIssuerWhenCallerOmitsIt(t *testing.T) {
	pub, priv := mustEd25519Pair(t)
	v, err := newV4PublicPair(t, pub, priv,
		WithExpectedIssuer("svc-A"),
		WithExpectedAudience("svc-B"),
	)
	require.NoError(t, err)

	tok, err := v.Sign(Claims{
		Audience:  []string{"svc-B"},
		ExpiresAt: futureExp(),
	}, priv)
	require.NoError(t, err)

	claims, err := v.Verify(tok, time.Now())
	require.NoError(t, err)
	assert.Equal(t, "svc-A", claims.Issuer)
}

func TestSeal_RejectsCallerAudienceMismatch(t *testing.T) {
	key := randomV4LocalKey(t)
	v, err := NewV4Local(key,
		WithExpectedIssuer("svc-A"),
		WithExpectedAudience("svc-B"),
	)
	require.NoError(t, err)

	_, err = v.Seal(Claims{
		Issuer:    "svc-A",
		Audience:  []string{"wrong-secret-token-audience"},
		ExpiresAt: futureExp(),
	})
	assert.ErrorIs(t, err, ErrAudienceUnknown)
	assert.NotContains(t, err.Error(), "wrong-secret-token-audience")
	assert.NotContains(t, err.Error(), "svc-B")
}

func TestSeal_StampsConfiguredAudienceWhenCallerOmitsIt(t *testing.T) {
	key := randomV4LocalKey(t)
	v, err := NewV4Local(key,
		WithExpectedIssuer("svc-A"),
		WithExpectedAudience("svc-B"),
	)
	require.NoError(t, err)

	tok, err := v.Seal(Claims{
		Issuer:    "svc-A",
		ExpiresAt: futureExp(),
	})
	require.NoError(t, err)

	claims, err := v.Verify(tok, time.Now())
	require.NoError(t, err)
	assert.Equal(t, []string{"svc-B"}, claims.Audience)
}

func TestWithClockSkewTolerance_PanicsOnNegative(t *testing.T) {
	assert.Panics(t, func() {
		_ = WithClockSkewTolerance(-time.Second)
	})
}

func TestSign_PropagatesCustomClaimWriteError(t *testing.T) {
	pub, priv := mustEd25519Pair(t)
	v, err := newV4PublicPair(t, pub, priv,
		WithExpectedIssuer("svc-A"),
		WithExpectedAudience("svc-B"),
	)
	require.NoError(t, err)

	_, err = v.Sign(Claims{
		Issuer:    "svc-A",
		Audience:  []string{"svc-B"},
		ExpiresAt: futureExp(),
		Custom:    map[string]any{"bad-secret-token-claim": func() {}},
	}, priv)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "set custom claim")
	assert.NotContains(t, err.Error(), "bad-secret-token-claim")
}

func TestSeal_PropagatesCustomClaimWriteError(t *testing.T) {
	key := randomV4LocalKey(t)
	v, err := NewV4Local(key,
		WithExpectedIssuer("svc-A"),
		WithExpectedAudience("svc-B"),
	)
	require.NoError(t, err)

	_, err = v.Seal(Claims{
		Issuer:    "svc-A",
		Audience:  []string{"svc-B"},
		ExpiresAt: futureExp(),
		Custom:    map[string]any{"bad-secret-token-claim": make(chan int)},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "set custom claim")
	assert.NotContains(t, err.Error(), "bad-secret-token-claim")
}

func TestV4PublicVerifier_InvalidReceiverReturnsError(t *testing.T) {
	for name, verifier := range map[string]*V4PublicVerifier{
		"nil":  nil,
		"zero": {},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := verifier.Verify("token", time.Now()); !errors.Is(err, ErrInvalidVerifier) {
				t.Fatalf("Verify error = %v, want ErrInvalidVerifier", err)
			}
		})
	}
}

func TestV4PublicSigner_InvalidReceiverReturnsError(t *testing.T) {
	for name, signer := range map[string]*V4PublicSigner{
		"nil":  nil,
		"zero": {},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := signer.Sign(Claims{ExpiresAt: futureExp()}); !errors.Is(err, ErrInvalidVerifier) {
				t.Fatalf("Sign error = %v, want ErrInvalidVerifier", err)
			}
		})
	}
}

func TestV4Local_InvalidReceiverReturnsError(t *testing.T) {
	for name, verifier := range map[string]*V4Local{
		"nil":  nil,
		"zero": {},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := verifier.Verify("token", time.Now()); !errors.Is(err, ErrInvalidVerifier) {
				t.Fatalf("Verify error = %v, want ErrInvalidVerifier", err)
			}
			if _, err := verifier.Seal(Claims{ExpiresAt: futureExp()}); !errors.Is(err, ErrInvalidVerifier) {
				t.Fatalf("Seal error = %v, want ErrInvalidVerifier", err)
			}
		})
	}
}

func TestV4PublicSigner_Close_ZeroesPrivateKeyAndFailsClosed(t *testing.T) {
	pub, priv := mustEd25519Pair(t)
	signer, err := NewV4PublicSigner(priv,
		WithExpectedIssuer("issuer"),
		WithExpectedAudience("aud"),
	)
	if err != nil {
		t.Fatalf("NewV4PublicSigner: %v", err)
	}

	// Sanity: signing works before Close.
	if _, err := signer.Sign(Claims{
		Issuer:    "issuer",
		Audience:  []string{"aud"},
		ExpiresAt: futureExp(),
	}); err != nil {
		t.Fatalf("Sign before Close: %v", err)
	}

	if err := signer.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Verify the underlying material was overwritten.
	raw := signer.priv.ExportBytes()
	zero := true
	for _, b := range raw {
		if b != 0 {
			zero = false
			break
		}
	}
	if !zero {
		t.Fatalf("private-key bytes not zeroed after Close: % x", raw)
	}

	// Subsequent Sign returns ErrSignerClosed.
	if _, err := signer.Sign(Claims{
		Issuer:    "issuer",
		Audience:  []string{"aud"},
		ExpiresAt: futureExp(),
	}); !errors.Is(err, ErrSignerClosed) {
		t.Fatalf("Sign after Close = %v, want ErrSignerClosed", err)
	}

	// Idempotent.
	if err := signer.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}

	// The verifier is unaffected — its public key has nothing to do
	// with the signer's wiped private key.
	_ = pub
}

func TestV4PublicSigner_Close_NilReceiverIsSafe(t *testing.T) {
	var s *V4PublicSigner
	if err := s.Close(); err != nil {
		t.Fatalf("nil Close: %v", err)
	}
}

// TestV4Local_AcceptsTokenWithoutExpWhenOptedOut mirrors the v4.public
// behaviour: WithoutExpiration must let the kit's validate() own exp
// handling. The bug was that NewV4Local seeded the upstream parser with
// NewParser(), whose preloaded NotExpired rule errors when exp is missing,
// so WithoutExpiration tokens were always rejected at parse time before
// validate() ran.
func TestV4Local_AcceptsTokenWithoutExpWhenOptedOut(t *testing.T) {
	key := randomV4LocalKey(t)
	v, err := NewV4Local(key,
		WithExpectedIssuer("svc-A"),
		WithExpectedAudience("svc-B"),
		WithoutExpiration(),
	)
	require.NoError(t, err)

	tok, err := v.Seal(Claims{Issuer: "svc-A", Audience: []string{"svc-B"}})
	require.NoError(t, err)

	claims, err := v.Verify(tok, time.Now())
	require.NoError(t, err)
	assert.True(t, claims.ExpiresAt.IsZero())
}

// TestV4Local_ClockSkewWithinTolerance asserts the kit's clock-skew
// tolerance governs v4.local exp checks. The upstream NotExpired rule
// compares exp to time.Now() with no tolerance and ignores the caller's
// now, so a token just inside the skew window was wrongly rejected.
func TestV4Local_ClockSkewWithinTolerance(t *testing.T) {
	key := randomV4LocalKey(t)
	v, err := NewV4Local(key,
		WithExpectedIssuer("svc-A"),
		WithExpectedAudience("svc-B"),
		WithClockSkewTolerance(30*time.Second),
	)
	require.NoError(t, err)

	now := time.Now()
	tok, err := v.Seal(Claims{
		Issuer:    "svc-A",
		Audience:  []string{"svc-B"},
		ExpiresAt: now.Add(-10 * time.Second),
	})
	require.NoError(t, err)

	// Inside the 30s skew window: accepted.
	_, err = v.Verify(tok, now)
	require.NoError(t, err)

	// Outside the window: rejected as expired (not as ErrTokenInvalid).
	_, err = v.Verify(tok, now.Add(time.Minute))
	assert.ErrorIs(t, err, ErrTokenExpired)
}

// TestV4Local_ExpiredReturnsErrTokenExpired ensures expired v4.local
// tokens surface the typed ErrTokenExpired (from validate) rather than
// the generic ErrTokenInvalid that the upstream NotExpired rule produced
// at parse time.
func TestV4Local_ExpiredReturnsErrTokenExpired(t *testing.T) {
	key := randomV4LocalKey(t)
	v, err := NewV4Local(key,
		WithExpectedIssuer("svc-A"),
		WithExpectedAudience("svc-B"),
	)
	require.NoError(t, err)

	now := time.Now()
	tok, err := v.Seal(Claims{
		Issuer:    "svc-A",
		Audience:  []string{"svc-B"},
		ExpiresAt: now.Add(-time.Minute),
	})
	require.NoError(t, err)

	_, err = v.Verify(tok, now)
	assert.ErrorIs(t, err, ErrTokenExpired)
}

// TestV4Local_UsesCallerSuppliedNow confirms the caller's now drives the
// exp comparison: a token expired against the wall clock but still valid
// against an earlier caller-supplied now must verify. The upstream
// NotExpired rule ignored now entirely and used time.Now().
func TestV4Local_UsesCallerSuppliedNow(t *testing.T) {
	key := randomV4LocalKey(t)
	v, err := NewV4Local(key,
		WithExpectedIssuer("svc-A"),
		WithExpectedAudience("svc-B"),
	)
	require.NoError(t, err)

	issued := time.Now().Add(-time.Hour)
	tok, err := v.Seal(Claims{
		Issuer:    "svc-A",
		Audience:  []string{"svc-B"},
		ExpiresAt: issued.Add(time.Minute), // expired vs wall clock
	})
	require.NoError(t, err)

	// Verifying as-of a time when the token was still live succeeds.
	_, err = v.Verify(tok, issued.Add(30*time.Second))
	require.NoError(t, err)
}

// TestV4Local_Close_ZeroesKeyMaterialAndFailsClosed pins the V4Local.Close
// contract: after Close, the kit-owned key copy is zeroed AND the wrapped
// upstream key's exported bytes are zeroed, and Seal/Verify fail closed
// with ErrV4LocalClosed. The doc previously claimed in-place wiping of the
// upstream material "covered by a tripwire test" — no such test existed.
func TestV4Local_Close_ZeroesKeyMaterialAndFailsClosed(t *testing.T) {
	key := randomV4LocalKey(t)
	v, err := NewV4Local(key,
		WithExpectedIssuer("svc-A"),
		WithExpectedAudience("svc-B"),
	)
	require.NoError(t, err)

	// Sanity: seal works before Close.
	tok, err := v.Seal(Claims{
		Issuer:    "svc-A",
		Audience:  []string{"svc-B"},
		ExpiresAt: futureExp(),
	})
	require.NoError(t, err)

	require.NoError(t, v.Close())

	// Kit-owned copy is zeroed.
	for _, b := range v.keyBytes {
		assert.Zero(t, b, "kit-owned key bytes not zeroed after Close")
	}
	// The wrapped upstream key's exported view is zeroed.
	raw := v.key.ExportBytes()
	for _, b := range raw {
		assert.Zero(t, b, "wrapped key bytes not zeroed after Close")
	}

	// Seal/Verify fail closed.
	_, err = v.Seal(Claims{
		Issuer:    "svc-A",
		Audience:  []string{"svc-B"},
		ExpiresAt: futureExp(),
	})
	assert.ErrorIs(t, err, ErrV4LocalClosed)

	_, err = v.Verify(tok, time.Now())
	assert.ErrorIs(t, err, ErrV4LocalClosed)

	// Idempotent.
	require.NoError(t, v.Close())
}

func TestV4Local_Close_NilReceiverIsSafe(t *testing.T) {
	var v *V4Local
	if err := v.Close(); err != nil {
		t.Fatalf("nil Close: %v", err)
	}
}

// TestV4Local_CloseConcurrentWithSealVerify pins keyMu: Close may race
// concurrent Seal/Verify without panicking; after Close completes, both
// fail closed with ErrV4LocalClosed.
func TestV4Local_CloseConcurrentWithSealVerify(t *testing.T) {
	key := randomV4LocalKey(t)
	v, err := NewV4Local(key,
		WithExpectedIssuer("svc-A"),
		WithExpectedAudience("svc-B"),
	)
	require.NoError(t, err)

	tok, err := v.Seal(Claims{
		Issuer: "svc-A", Audience: []string{"svc-B"}, ExpiresAt: futureExp(),
	})
	require.NoError(t, err)

	var wg sync.WaitGroup
	errCh := make(chan error, 64)
	for i := 0; i < 16; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, serr := v.Seal(Claims{
				Issuer: "svc-A", Audience: []string{"svc-B"}, ExpiresAt: futureExp(),
			})
			if serr != nil && !errors.Is(serr, ErrV4LocalClosed) {
				errCh <- serr
			}
		}()
		go func() {
			defer wg.Done()
			_, verr := v.Verify(tok, time.Now())
			if verr != nil && !errors.Is(verr, ErrV4LocalClosed) {
				errCh <- verr
			}
		}()
	}

	closeDone := make(chan error, 1)
	go func() { closeDone <- v.Close() }()

	wg.Wait()
	require.NoError(t, <-closeDone)
	close(errCh)
	for e := range errCh {
		t.Fatalf("unexpected concurrent error: %v", e)
	}

	_, err = v.Seal(Claims{
		Issuer: "svc-A", Audience: []string{"svc-B"}, ExpiresAt: futureExp(),
	})
	assert.ErrorIs(t, err, ErrV4LocalClosed)
	_, err = v.Verify(tok, time.Now())
	assert.ErrorIs(t, err, ErrV4LocalClosed)
}

// TestV4PublicSigner_CloseConcurrentWithSign exercises the documented
// "safe for concurrent use" contract: Close zeroes the live Ed25519
// private-key bytes that an in-flight Sign reads inside ed25519.Sign.
// Without synchronisation this is a data race (and can emit a token
// signed with partially zeroed material). Run with -race to catch it.
// Any token Sign returns successfully must verify against the public key;
// once Close has completed, Sign must return ErrSignerClosed.
func TestV4PublicSigner_CloseConcurrentWithSign(t *testing.T) {
	pub, priv := mustEd25519Pair(t)
	signer, err := NewV4PublicSigner(priv,
		WithExpectedIssuer("svc-A"),
		WithExpectedAudience("svc-B"),
	)
	require.NoError(t, err)

	verifier, err := NewV4PublicVerifier([]ed25519.PublicKey{pub},
		WithExpectedIssuer("svc-A"),
		WithExpectedAudience("svc-B"),
	)
	require.NoError(t, err)

	const signers = 8
	start := make(chan struct{})
	done := make(chan struct{})

	for i := 0; i < signers; i++ {
		go func() {
			defer func() { done <- struct{}{} }()
			<-start
			for {
				tok, serr := signer.Sign(Claims{
					Issuer:    "svc-A",
					Audience:  []string{"svc-B"},
					ExpiresAt: time.Now().Add(time.Hour),
				})
				if errors.Is(serr, ErrSignerClosed) {
					return
				}
				if serr != nil {
					// No other error is expected from a valid signer.
					t.Errorf("Sign: %v", serr)
					return
				}
				// Any successfully signed token must verify: a token
				// produced from partially zeroed key material would fail
				// authentication here.
				if _, verr := verifier.Verify(tok, time.Now()); verr != nil {
					t.Errorf("token from concurrent Sign failed Verify: %v", verr)
					return
				}
			}
		}()
	}

	// Close concurrently with the in-flight signers (not after them) so
	// the zero-write overlaps an active Sign reading the same key bytes.
	closeDone := make(chan error, 1)
	go func() {
		<-start
		closeDone <- signer.Close()
	}()

	close(start)
	require.NoError(t, <-closeDone)

	for i := 0; i < signers; i++ {
		<-done
	}

	// After Close, signing fails closed.
	_, err = signer.Sign(Claims{
		Issuer:    "svc-A",
		Audience:  []string{"svc-B"},
		ExpiresAt: futureExp(),
	})
	assert.ErrorIs(t, err, ErrSignerClosed)
}
