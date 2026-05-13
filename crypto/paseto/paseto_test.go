package paseto

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
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
