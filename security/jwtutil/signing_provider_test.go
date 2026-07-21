package jwtutil

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lestrrat-go/jwx/v3/jwa"
	"github.com/lestrrat-go/jwx/v3/jwk"
)

func mustECDSAKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate ecdsa key: %v", err)
	}
	return k
}

func mustRSAKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate rsa key: %v", err)
	}
	return k
}

func staticRotator(k crypto.PrivateKey) KeyRotator {
	return func(_ context.Context) (crypto.PrivateKey, error) { return k, nil }
}

// verifierForKey builds a single-entry JWKS for pub. The kid is derived
// from the RFC 7638 thumbprint so it matches the SigningProvider's
// default thumbprint-kid path.
func verifierForKey(t *testing.T, pub any, alg jwa.SignatureAlgorithm) *KeySet {
	t.Helper()
	pubJWK, err := jwk.Import(pub)
	if err != nil {
		t.Fatalf("jwk.Import public: %v", err)
	}
	if err := jwk.AssignKeyID(pubJWK); err != nil {
		t.Fatalf("assign kid: %v", err)
	}
	if err := pubJWK.Set(jwk.AlgorithmKey, alg); err != nil {
		t.Fatalf("set alg: %v", err)
	}
	if err := pubJWK.Set(jwk.KeyUsageKey, "sig"); err != nil {
		t.Fatalf("set use: %v", err)
	}
	set := jwk.NewSet()
	if err := set.AddKey(pubJWK); err != nil {
		t.Fatalf("add key: %v", err)
	}
	data, err := json.Marshal(set)
	if err != nil {
		t.Fatalf("marshal jwks: %v", err)
	}
	ks, err := ParseKeySet(data)
	if err != nil {
		t.Fatalf("ParseKeySet: %v", err)
	}
	// Test helper defaults to explicit AllowAny*; call sites that pin
	// ExpectedIssuer/Audience override via field assignment (Expected wins
	// over AllowAny at freeze time).
	ks.AllowAnyIssuer = true
	ks.AllowAnyAudience = true
	return ks
}

func TestNewSigningProvider_RejectsNilContext(t *testing.T) {
	_, err := NewSigningProvider(nilContextForTest(), staticRotator(mustECDSAKey(t)), WithSigningRotationInterval(time.Hour),
		WithSigningExpectedIssuer("svc"), WithSigningAllowAnyAudience(),
	)
	if err == nil {
		t.Fatal("expected error on nil context")
	}
}

func TestNewSigningProvider_RejectsNilRotator(t *testing.T) {
	_, err := NewSigningProvider(context.Background(), nil, WithSigningRotationInterval(time.Hour),
		WithSigningExpectedIssuer("svc"), WithSigningAllowAnyAudience(),
	)
	if err == nil {
		t.Fatal("expected error on nil rotator")
	}
}

func TestNewSigningProvider_RequiresRotationInterval(t *testing.T) {
	_, err := NewSigningProvider(context.Background(), staticRotator(mustECDSAKey(t)),
		WithSigningExpectedIssuer("svc"), WithSigningAllowAnyAudience(),
	)
	if err == nil {
		t.Fatal("expected error when WithSigningRotationInterval is omitted")
	}
}

func TestWithSigningRotationInterval_PanicsOnZero(t *testing.T) {
	defer func() {
		if rec := recover(); rec == nil {
			t.Fatal("expected panic on zero duration")
		}
	}()
	_ = WithSigningRotationInterval(0)
}

func TestNewSigningProvider_RejectsNilOption(t *testing.T) {
	_, err := NewSigningProvider(context.Background(), staticRotator(mustECDSAKey(t)), WithSigningRotationInterval(time.Hour), nil)
	if err == nil {
		t.Fatal("expected error on nil option")
	}
}

func TestNewSigningProvider_RequiresIssuerOrAllowAny(t *testing.T) {
	_, err := NewSigningProvider(context.Background(), staticRotator(mustECDSAKey(t)), WithSigningRotationInterval(time.Hour),
		WithSigningExpectedAudience("aud"),
	)
	if err == nil {
		t.Fatal("expected error when issuer guardrail missing")
	}
}

func TestNewSigningProvider_RequiresAudienceOrAllowAny(t *testing.T) {
	_, err := NewSigningProvider(context.Background(), staticRotator(mustECDSAKey(t)), WithSigningRotationInterval(time.Hour),
		WithSigningExpectedIssuer("svc"),
	)
	if err == nil {
		t.Fatal("expected error when audience guardrail missing")
	}
}

func TestNewSigningProvider_PropagatesInitialLoadFailure(t *testing.T) {
	boom := errors.New("kms unavailable")
	_, err := NewSigningProvider(context.Background(),
		func(_ context.Context) (crypto.PrivateKey, error) { return nil, boom },
		WithSigningRotationInterval(time.Hour),
		WithSigningExpectedIssuer("svc"), WithSigningAllowAnyAudience(),
	)
	if !errors.Is(err, boom) {
		t.Fatalf("expected boom error, got %v", err)
	}
}

func TestNewSigningProvider_RejectsNilKey(t *testing.T) {
	_, err := NewSigningProvider(context.Background(),
		func(_ context.Context) (crypto.PrivateKey, error) { return nil, nil },
		WithSigningRotationInterval(time.Hour),
		WithSigningExpectedIssuer("svc"), WithSigningAllowAnyAudience(),
	)
	if err == nil {
		t.Fatal("expected error on nil key")
	}
}

func TestNewSigningProvider_RejectsHMACAlg(t *testing.T) {
	for _, alg := range []jwa.SignatureAlgorithm{jwa.HS256(), jwa.HS384(), jwa.HS512()} {
		alg := alg
		t.Run(alg.String(), func(t *testing.T) {
			_, err := NewSigningProvider(context.Background(),
				staticRotator(mustECDSAKey(t)),
				WithSigningRotationInterval(time.Hour),
				WithSigningExpectedIssuer("svc"), WithSigningAllowAnyAudience(),
				WithSigningMethod(alg),
			)
			if err == nil || !strings.Contains(err.Error(), "symmetric") {
				t.Fatalf("expected symmetric rejection, got %v", err)
			}
		})
	}
}

func TestNewSigningProvider_RejectsNoneAlg(t *testing.T) {
	_, err := NewSigningProvider(context.Background(),
		staticRotator(mustECDSAKey(t)),
		WithSigningRotationInterval(time.Hour),
		WithSigningExpectedIssuer("svc"), WithSigningAllowAnyAudience(),
		WithSigningMethod(jwa.NoSignature()),
	)
	if err == nil || !strings.Contains(err.Error(), "none") {
		t.Fatalf("expected \"none\" rejection, got %v", err)
	}
}

func TestSigningProvider_RejectsSymmetricKeyMaterial(t *testing.T) {
	// Rotator returns []byte, which jwk.Import wraps as a symmetric
	// key. The refresh path must reject that even though the configured
	// algorithm is asymmetric, so a caller cannot smuggle HMAC key bytes
	// past the constructor's alg check.
	rawHMACSecret := []byte("not-a-real-secret-but-32-bytes!!")
	_, err := NewSigningProvider(context.Background(),
		func(_ context.Context) (crypto.PrivateKey, error) { return rawHMACSecret, nil },
		WithSigningRotationInterval(time.Hour),
		WithSigningExpectedIssuer("svc"), WithSigningAllowAnyAudience(),
		WithSigningDefaultLifetime(time.Minute),
	)
	if err == nil || !strings.Contains(err.Error(), "symmetric") {
		t.Fatalf("expected symmetric-key rejection, got %v", err)
	}
}

func TestSigningProvider_SignsAndVerifies_ES256(t *testing.T) {
	priv := mustECDSAKey(t)
	p, err := NewSigningProvider(context.Background(), staticRotator(priv), WithSigningRotationInterval(time.Hour),
		WithSigningExpectedIssuer("svc"),
		WithSigningExpectedAudience("aud-A"),
		WithSigningDefaultLifetime(5*time.Minute),
	)
	if err != nil {
		t.Fatalf("NewSigningProvider: %v", err)
	}
	defer func() { _ = p.Close() }()

	tok, err := p.Sign(Claims{Subject: "user-1"})
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}

	ks := verifierForKey(t, priv.Public(), jwa.ES256())
	ks.ExpectedIssuer = "svc"
	ks.ExpectedAudience = "aud-A"
	claims, err := ks.Verify(tok, time.Now())
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if claims.Subject != "user-1" {
		t.Fatalf("subject = %q, want user-1", claims.Subject)
	}
	if claims.Issuer != "svc" {
		t.Fatalf("issuer = %q, want svc", claims.Issuer)
	}
	if claims.ID == "" {
		t.Fatal("expected non-empty random jti")
	}
}

func TestSigningProvider_SignsAndVerifies_RS256(t *testing.T) {
	priv := mustRSAKey(t)
	p, err := NewSigningProvider(context.Background(), staticRotator(priv), WithSigningRotationInterval(time.Hour),
		WithSigningExpectedIssuer("svc"),
		WithSigningExpectedAudience("aud-A"),
		WithSigningDefaultLifetime(5*time.Minute),
		WithSigningMethod(jwa.RS256()),
	)
	if err != nil {
		t.Fatalf("NewSigningProvider: %v", err)
	}
	defer func() { _ = p.Close() }()

	tok, err := p.Sign(Claims{Subject: "user-rs"})
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}

	ks := verifierForKey(t, &priv.PublicKey, jwa.RS256())
	ks.ExpectedIssuer = "svc"
	ks.ExpectedAudience = "aud-A"
	claims, err := ks.Verify(tok, time.Now())
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if claims.Subject != "user-rs" {
		t.Fatalf("subject = %q", claims.Subject)
	}
}

func TestSigningProvider_SignsAndVerifies_EdDSA(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("ed25519: %v", err)
	}
	p, err := NewSigningProvider(context.Background(), staticRotator(priv), WithSigningRotationInterval(time.Hour),
		WithSigningExpectedIssuer("svc"),
		WithSigningExpectedAudience("aud-A"),
		WithSigningDefaultLifetime(5*time.Minute),
		WithSigningMethod(jwa.EdDSA()),
	)
	if err != nil {
		t.Fatalf("NewSigningProvider: %v", err)
	}
	defer func() { _ = p.Close() }()

	tok, err := p.Sign(Claims{Subject: "user-ed"})
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}

	ks := verifierForKey(t, priv.Public(), jwa.EdDSA())
	ks.ExpectedIssuer = "svc"
	ks.ExpectedAudience = "aud-A"
	if _, err := ks.Verify(tok, time.Now()); err != nil {
		t.Fatalf("Verify: %v", err)
	}
}

func TestSigningProvider_RejectsEmptySubject(t *testing.T) {
	p, err := NewSigningProvider(context.Background(), staticRotator(mustECDSAKey(t)), WithSigningRotationInterval(time.Hour),
		WithSigningExpectedIssuer("svc"), WithSigningAllowAnyAudience(),
		WithSigningDefaultLifetime(time.Minute),
	)
	if err != nil {
		t.Fatalf("NewSigningProvider: %v", err)
	}
	defer func() { _ = p.Close() }()

	if _, err := p.Sign(Claims{}); err == nil {
		t.Fatal("expected error on empty subject")
	}
}

func TestSigningProvider_RequiresExpOrDefaultLifetime(t *testing.T) {
	p, err := NewSigningProvider(context.Background(), staticRotator(mustECDSAKey(t)), WithSigningRotationInterval(time.Hour),
		WithSigningExpectedIssuer("svc"), WithSigningAllowAnyAudience(),
	)
	if err != nil {
		t.Fatalf("NewSigningProvider: %v", err)
	}
	defer func() { _ = p.Close() }()

	if _, err := p.Sign(Claims{Subject: "alice"}); err == nil {
		t.Fatal("expected error when neither ExpiresAt nor default lifetime is set")
	}
}

func TestSigningProvider_HonoursCallerJTI(t *testing.T) {
	priv := mustECDSAKey(t)
	p, err := NewSigningProvider(context.Background(), staticRotator(priv), WithSigningRotationInterval(time.Hour),
		WithSigningExpectedIssuer("svc"),
		WithSigningExpectedAudience("aud"),
		WithSigningDefaultLifetime(time.Minute),
	)
	if err != nil {
		t.Fatalf("NewSigningProvider: %v", err)
	}
	defer func() { _ = p.Close() }()

	tok, err := p.Sign(Claims{Subject: "alice", ID: "fixed-jti"})
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	ks := verifierForKey(t, priv.Public(), jwa.ES256())
	ks.ExpectedIssuer = "svc"
	ks.ExpectedAudience = "aud"
	claims, err := ks.Verify(tok, time.Now())
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if claims.ID != "fixed-jti" {
		t.Fatalf("jti = %q, want fixed-jti", claims.ID)
	}
}

func TestSigningProvider_MintsRandomJTIByDefault(t *testing.T) {
	priv := mustECDSAKey(t)
	p, err := NewSigningProvider(context.Background(), staticRotator(priv), WithSigningRotationInterval(time.Hour),
		WithSigningExpectedIssuer("svc"),
		WithSigningExpectedAudience("aud"),
		WithSigningDefaultLifetime(time.Minute),
	)
	if err != nil {
		t.Fatalf("NewSigningProvider: %v", err)
	}
	defer func() { _ = p.Close() }()

	seen := make(map[string]struct{})
	ks := verifierForKey(t, priv.Public(), jwa.ES256())
	ks.ExpectedIssuer = "svc"
	ks.ExpectedAudience = "aud"
	for i := 0; i < 10; i++ {
		tok, err := p.Sign(Claims{Subject: "alice"})
		if err != nil {
			t.Fatalf("Sign: %v", err)
		}
		claims, err := ks.Verify(tok, time.Now())
		if err != nil {
			t.Fatalf("Verify: %v", err)
		}
		if claims.ID == "" {
			t.Fatal("expected jti to be set")
		}
		if _, dup := seen[claims.ID]; dup {
			t.Fatalf("duplicate jti %q across distinct Sign calls", claims.ID)
		}
		seen[claims.ID] = struct{}{}
	}
}

func TestSigningProvider_RefreshSwapsKey(t *testing.T) {
	old := mustECDSAKey(t)
	fresh := mustECDSAKey(t)
	var current atomic.Pointer[ecdsa.PrivateKey]
	current.Store(old)

	p, err := NewSigningProvider(context.Background(),
		func(_ context.Context) (crypto.PrivateKey, error) { return current.Load(), nil },
		WithSigningRotationInterval(time.Hour),
		WithSigningExpectedIssuer("svc"),
		WithSigningExpectedAudience("aud"),
		WithSigningDefaultLifetime(time.Minute),
	)
	if err != nil {
		t.Fatalf("NewSigningProvider: %v", err)
	}
	defer func() { _ = p.Close() }()

	tokOld, err := p.Sign(Claims{Subject: "alice"})
	if err != nil {
		t.Fatalf("Sign old: %v", err)
	}
	ksOld := verifierForKey(t, old.Public(), jwa.ES256())
	ksOld.ExpectedIssuer = "svc"
	ksOld.ExpectedAudience = "aud"
	if _, err := ksOld.Verify(tokOld, time.Now()); err != nil {
		t.Fatalf("verify old: %v", err)
	}

	// Force a refresh outside the ticker path so the test is
	// deterministic.
	current.Store(fresh)
	if err := p.refresh(context.Background()); err != nil {
		t.Fatalf("refresh: %v", err)
	}

	tokNew, err := p.Sign(Claims{Subject: "bob"})
	if err != nil {
		t.Fatalf("Sign new: %v", err)
	}
	ksNew := verifierForKey(t, fresh.Public(), jwa.ES256())
	ksNew.ExpectedIssuer = "svc"
	ksNew.ExpectedAudience = "aud"
	claims, err := ksNew.Verify(tokNew, time.Now())
	if err != nil {
		t.Fatalf("verify new: %v", err)
	}
	if claims.Subject != "bob" {
		t.Fatalf("subject = %q", claims.Subject)
	}

	// Tokens signed with the previous key must NOT verify under the
	// new public key — cryptographic property check, mirrors the
	// paseto test.
	if _, err := ksNew.Verify(tokOld, time.Now()); err == nil {
		t.Fatal("expected old token to fail verification under new key")
	}
}

func ed25519PrivForTest(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("ed25519: %v", err)
	}
	return priv
}

// TestSigningProvider_RefreshRejectsWrongShapeKey covers the rotation
// hazard: a KeyRotator that hands back a key whose shape does not match
// the configured algorithm (e.g. an RSA key for an ES256 provider) must
// be rejected by refresh so the OnSigningRefreshError callback fires and
// the previously-good key is retained. Without the guard, refresh would
// "succeed" silently, overwrite the good key, and poison every later
// Sign forever.
func TestSigningProvider_RefreshRejectsWrongShapeKey(t *testing.T) {
	cases := []struct {
		name    string
		alg     jwa.SignatureAlgorithm
		good    func(*testing.T) crypto.PrivateKey
		wrong   func(*testing.T) crypto.PrivateKey
		wantKty string
	}{
		{
			name:    "ES256_rotated_to_RSA",
			alg:     jwa.ES256(),
			good:    func(t *testing.T) crypto.PrivateKey { return mustECDSAKey(t) },
			wrong:   func(t *testing.T) crypto.PrivateKey { return mustRSAKey(t) },
			wantKty: "RSA",
		},
		{
			name:    "RS256_rotated_to_EC",
			alg:     jwa.RS256(),
			good:    func(t *testing.T) crypto.PrivateKey { return mustRSAKey(t) },
			wrong:   func(t *testing.T) crypto.PrivateKey { return mustECDSAKey(t) },
			wantKty: "EC",
		},
		{
			name:    "EdDSA_rotated_to_EC",
			alg:     jwa.EdDSA(),
			good:    func(t *testing.T) crypto.PrivateKey { return ed25519PrivForTest(t) },
			wrong:   func(t *testing.T) crypto.PrivateKey { return mustECDSAKey(t) },
			wantKty: "EC",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			good := tc.good(t)
			wrong := tc.wrong(t)
			var cur atomic.Pointer[crypto.PrivateKey]
			cur.Store(&good)
			src := func(_ context.Context) (crypto.PrivateKey, error) { return *cur.Load(), nil }

			var cbErr atomic.Pointer[error]
			p, err := NewSigningProvider(context.Background(), src,
				WithSigningRotationInterval(time.Hour),
				WithSigningExpectedIssuer("svc"), WithSigningAllowAnyAudience(),
				WithSigningDefaultLifetime(time.Minute),
				WithSigningMethod(tc.alg),
				WithOnSigningRefreshError(func(e error) { cbErr.Store(&e) }),
			)
			if err != nil {
				t.Fatalf("NewSigningProvider: %v", err)
			}
			defer func() { _ = p.Close() }()

			// Baseline: the good key signs fine.
			if _, err := p.Sign(Claims{Subject: "alice"}); err != nil {
				t.Fatalf("initial Sign: %v", err)
			}

			// Rotate to a wrong-shape key. refresh must reject it.
			cur.Store(&wrong)
			rerr := p.refresh(context.Background())
			if rerr == nil {
				t.Fatalf("refresh accepted a %s key for alg %s; want rejection", tc.wantKty, tc.alg)
			}
			if !strings.Contains(rerr.Error(), "incompatible") {
				t.Fatalf("refresh error = %v, want it to mention incompatibility", rerr)
			}

			// The loop path surfaces refresh errors via the callback;
			// exercise that wiring directly so a stalled rotation alerts.
			p.callOnRefreshError(rerr)
			if got := cbErr.Load(); got == nil || *got == nil {
				t.Fatal("OnSigningRefreshError callback was not invoked for the wrong-shape rotation")
			}

			// The previously-good key must be retained: Sign still works.
			if _, err := p.Sign(Claims{Subject: "bob"}); err != nil {
				t.Fatalf("Sign after rejected rotation should use the retained key, got: %v", err)
			}
		})
	}
}

// TestNewSigningProvider_RejectsWrongShapeInitialKey locks the documented
// "validates compatibility once at construction" contract (see KeyRotator
// doc). A rotator that returns a key whose shape mismatches the configured
// algorithm on the very first load must fail the CONSTRUCTOR fast, not defer
// the failure to a runtime "jwtutil: sign token" error on every later Sign.
// The constructor runs the same keyTypeForSigningAlg guard via refresh, so
// the initial load surfaces the mismatch as NewSigningProvider's error return.
func TestNewSigningProvider_RejectsWrongShapeInitialKey(t *testing.T) {
	cases := []struct {
		name    string
		alg     jwa.SignatureAlgorithm
		wrong   func(*testing.T) crypto.PrivateKey
		wantKty string
	}{
		{
			name:    "ES256_with_RSA_key",
			alg:     jwa.ES256(),
			wrong:   func(t *testing.T) crypto.PrivateKey { return mustRSAKey(t) },
			wantKty: "RSA",
		},
		{
			name:    "RS256_with_EC_key",
			alg:     jwa.RS256(),
			wrong:   func(t *testing.T) crypto.PrivateKey { return mustECDSAKey(t) },
			wantKty: "EC",
		},
		{
			name:    "EdDSA_with_EC_key",
			alg:     jwa.EdDSA(),
			wrong:   func(t *testing.T) crypto.PrivateKey { return mustECDSAKey(t) },
			wantKty: "EC",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			p, err := NewSigningProvider(context.Background(),
				staticRotator(tc.wrong(t)),
				WithSigningRotationInterval(time.Hour),
				WithSigningExpectedIssuer("svc"), WithSigningAllowAnyAudience(),
				WithSigningMethod(tc.alg),
			)
			if err == nil {
				if p != nil {
					_ = p.Close()
				}
				t.Fatalf("NewSigningProvider accepted a %s key for alg %s; want a fast-fail construction error", tc.wantKty, tc.alg)
			}
			if p != nil {
				t.Fatalf("NewSigningProvider returned a non-nil provider alongside an error: %v", err)
			}
			if !strings.Contains(err.Error(), "incompatible") {
				t.Fatalf("construction error = %v, want it to mention incompatibility", err)
			}
		})
	}
}

func TestSigningProvider_RejectsSignAfterMaxStale(t *testing.T) {
	priv := mustECDSAKey(t)
	called := atomic.Bool{}
	src := func(_ context.Context) (crypto.PrivateKey, error) {
		if called.Swap(true) {
			return nil, errors.New("kms blip")
		}
		return priv, nil
	}
	fixed := time.Now()
	clock := func() time.Time { return fixed }
	p, err := NewSigningProvider(context.Background(), src, WithSigningRotationInterval(time.Hour),
		WithSigningExpectedIssuer("svc"), WithSigningAllowAnyAudience(),
		WithSigningDefaultLifetime(time.Minute),
		WithSigningMaxStale(time.Minute),
		withSigningProviderClock(clock),
		WithOnSigningRefreshError(func(error) {}),
	)
	if err != nil {
		t.Fatalf("NewSigningProvider: %v", err)
	}
	defer func() { _ = p.Close() }()

	if _, err := p.Sign(Claims{Subject: "alice"}); err != nil {
		t.Fatalf("Sign within stale window: %v", err)
	}
	fixed = fixed.Add(2 * time.Minute)
	_, err = p.Sign(Claims{Subject: "alice"})
	if !errors.Is(err, ErrKeySetUnavailable) {
		t.Fatalf("expected ErrKeySetUnavailable, got %v", err)
	}
	if !errors.Is(err, ErrSigningKeyUnavailable) {
		t.Fatalf("expected ErrSigningKeyUnavailable wrap, got %v", err)
	}
}

func TestSigningProvider_AfterCloseReturnsProviderClosed(t *testing.T) {
	p, err := NewSigningProvider(context.Background(), staticRotator(mustECDSAKey(t)), WithSigningRotationInterval(time.Hour),
		WithSigningExpectedIssuer("svc"), WithSigningAllowAnyAudience(),
		WithSigningDefaultLifetime(time.Minute),
	)
	if err != nil {
		t.Fatalf("NewSigningProvider: %v", err)
	}
	if err := p.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := p.Sign(Claims{Subject: "alice"}); !errors.Is(err, ErrSigningProviderClosed) {
		t.Fatalf("expected ErrSigningProviderClosed, got %v", err)
	}
}

func TestSigningProvider_AfterCloseReportsClosedEvenWhenCurrentNil(t *testing.T) {
	// Close stores closed=true before it nils current. The Sign path
	// re-checks the closed flag after observing current==nil so callers
	// branching on errors.Is(err, ErrSigningProviderClosed) keep seeing
	// the closed sentinel regardless of which atomic Sign observes first.
	p, err := NewSigningProvider(context.Background(), staticRotator(mustECDSAKey(t)), WithSigningRotationInterval(time.Hour),
		WithSigningExpectedIssuer("svc"), WithSigningAllowAnyAudience(),
		WithSigningDefaultLifetime(time.Minute),
	)
	if err != nil {
		t.Fatalf("NewSigningProvider: %v", err)
	}
	if err := p.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// After Close, current is nil. Sign must report ErrSigningProviderClosed.
	if _, err := p.Sign(Claims{Subject: "alice"}); !errors.Is(err, ErrSigningProviderClosed) {
		t.Fatalf("expected ErrSigningProviderClosed, got %v", err)
	}
}

func TestSigningProvider_CloseIsIdempotent(t *testing.T) {
	p, err := NewSigningProvider(context.Background(), staticRotator(mustECDSAKey(t)), WithSigningRotationInterval(time.Hour),
		WithSigningExpectedIssuer("svc"), WithSigningAllowAnyAudience(),
		WithSigningDefaultLifetime(time.Minute),
	)
	if err != nil {
		t.Fatalf("NewSigningProvider: %v", err)
	}
	if err := p.Close(); err != nil {
		t.Fatalf("Close #1: %v", err)
	}
	if err := p.Close(); err != nil {
		t.Fatalf("Close #2: %v", err)
	}
}

func TestSigningProvider_OnRefreshErrorCallbackPanicSwallowed(t *testing.T) {
	priv := mustECDSAKey(t)
	calls := atomic.Int64{}
	src := func(_ context.Context) (crypto.PrivateKey, error) {
		if calls.Add(1) == 1 {
			return priv, nil
		}
		return nil, errors.New("post-init failure")
	}

	cbInvoked := make(chan struct{}, 1)
	p, err := NewSigningProvider(context.Background(), src, WithSigningRotationInterval(time.Hour),
		WithSigningExpectedIssuer("svc"), WithSigningAllowAnyAudience(),
		WithSigningDefaultLifetime(time.Minute),
		WithOnSigningRefreshError(func(err error) {
			defer func() { _ = recover() }()
			select {
			case cbInvoked <- struct{}{}:
			default:
			}
			panic("intentional panic from test callback")
		}),
	)
	if err != nil {
		t.Fatalf("NewSigningProvider: %v", err)
	}
	defer func() { _ = p.Close() }()

	p.callOnRefreshError(errors.New("simulated"))
	select {
	case <-cbInvoked:
	case <-time.After(time.Second):
		t.Fatal("OnSigningRefreshError callback was not invoked")
	}
}

type recorderStub struct {
	mu    sync.Mutex
	calls []struct {
		Issuer, JTI string
		ExpiresAt   time.Time
	}
	err error
}

func (r *recorderStub) RecordIssued(_ context.Context, issuer, jti string, expiresAt time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, struct {
		Issuer, JTI string
		ExpiresAt   time.Time
	}{issuer, jti, expiresAt})
	return r.err
}

func (r *recorderStub) snapshot() []struct {
	Issuer, JTI string
	ExpiresAt   time.Time
} {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]struct {
		Issuer, JTI string
		ExpiresAt   time.Time
	}, len(r.calls))
	copy(out, r.calls)
	return out
}

func TestSigningProvider_WithIssuedJTIRecorder_RecordsEachSignedToken(t *testing.T) {
	priv := mustECDSAKey(t)
	rec := &recorderStub{}
	p, err := NewSigningProvider(context.Background(), staticRotator(priv), WithSigningRotationInterval(time.Hour),
		WithSigningExpectedIssuer("svc"),
		WithSigningExpectedAudience("aud"),
		WithSigningDefaultLifetime(time.Minute),
		WithIssuedJTIRecorder(rec),
	)
	if err != nil {
		t.Fatalf("NewSigningProvider: %v", err)
	}
	defer func() { _ = p.Close() }()

	if _, err := p.Sign(Claims{Subject: "alice", ID: "jti-A"}); err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if _, err := p.Sign(Claims{Subject: "alice", ID: "jti-B"}); err != nil {
		t.Fatalf("Sign: %v", err)
	}
	got := rec.snapshot()
	if len(got) != 2 {
		t.Fatalf("recorder calls = %d, want 2", len(got))
	}
	if got[0].JTI != "jti-A" || got[1].JTI != "jti-B" {
		t.Fatalf("recorder jtis = %v", got)
	}
	for i, c := range got {
		if c.Issuer != "svc" {
			t.Fatalf("call %d issuer = %q", i, c.Issuer)
		}
		if c.ExpiresAt.IsZero() {
			t.Fatalf("call %d zero expiresAt", i)
		}
	}
}

func TestSigningProvider_WithIssuedJTIRecorder_PropagatesError(t *testing.T) {
	priv := mustECDSAKey(t)
	boom := errors.New("ledger down")
	rec := &recorderStub{err: boom}
	p, err := NewSigningProvider(context.Background(), staticRotator(priv), WithSigningRotationInterval(time.Hour),
		WithSigningExpectedIssuer("svc"),
		WithSigningExpectedAudience("aud"),
		WithSigningDefaultLifetime(time.Minute),
		WithIssuedJTIRecorder(rec),
	)
	if err != nil {
		t.Fatalf("NewSigningProvider: %v", err)
	}
	defer func() { _ = p.Close() }()

	_, err = p.Sign(Claims{Subject: "alice"})
	if !errors.Is(err, boom) {
		t.Fatalf("expected recorder error, got %v", err)
	}
}

func TestSigningProvider_RaceSignAndRefresh(t *testing.T) {
	priv := mustECDSAKey(t)
	p, err := NewSigningProvider(context.Background(), staticRotator(priv), WithSigningRotationInterval(time.Hour),
		WithSigningExpectedIssuer("svc"),
		WithSigningExpectedAudience("aud"),
		WithSigningDefaultLifetime(time.Minute),
	)
	if err != nil {
		t.Fatalf("NewSigningProvider: %v", err)
	}
	defer func() { _ = p.Close() }()

	var wg sync.WaitGroup
	stop := make(chan struct{})
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				_, _ = p.Sign(Claims{Subject: "alice"})
			}
		}()
	}
	for i := 0; i < 20; i++ {
		if err := p.refresh(context.Background()); err != nil {
			t.Errorf("refresh: %v", err)
		}
	}
	close(stop)
	wg.Wait()
}

func TestSigningProvider_PermissionsAndScopesRoundTrip(t *testing.T) {
	priv := mustECDSAKey(t)
	p, err := NewSigningProvider(context.Background(), staticRotator(priv), WithSigningRotationInterval(time.Hour),
		WithSigningExpectedIssuer("svc"),
		WithSigningExpectedAudience("aud"),
		WithSigningDefaultLifetime(time.Minute),
	)
	if err != nil {
		t.Fatalf("NewSigningProvider: %v", err)
	}
	defer func() { _ = p.Close() }()

	tok, err := p.Sign(Claims{
		Subject:     "alice",
		Permissions: []string{"read:doc", "write:doc"},
		Scopes:      "openid profile",
	})
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	ks := verifierForKey(t, priv.Public(), jwa.ES256())
	ks.ExpectedIssuer = "svc"
	ks.ExpectedAudience = "aud"
	claims, err := ks.Verify(tok, time.Now())
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if len(claims.Permissions) != 2 || claims.Permissions[0] != "read:doc" {
		t.Fatalf("permissions = %v", claims.Permissions)
	}
	if claims.Scopes != "openid profile" {
		t.Fatalf("scopes = %q", claims.Scopes)
	}
}

func TestSigningProvider_AllowAnyIssuerAudienceOmitsClaims(t *testing.T) {
	priv := mustECDSAKey(t)
	p, err := NewSigningProvider(context.Background(), staticRotator(priv), WithSigningRotationInterval(time.Hour),
		WithSigningAllowAnyIssuer(),
		WithSigningAllowAnyAudience(),
		WithSigningDefaultLifetime(time.Minute),
	)
	if err != nil {
		t.Fatalf("NewSigningProvider: %v", err)
	}
	defer func() { _ = p.Close() }()

	tok, err := p.Sign(Claims{Subject: "alice"})
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	ks := verifierForKey(t, priv.Public(), jwa.ES256())
	// Verifier-side iss/aud left empty so the verifier accepts the
	// no-iss/no-aud token.
	claims, err := ks.Verify(tok, time.Now())
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if claims.Issuer != "" {
		t.Fatalf("issuer = %q, want empty", claims.Issuer)
	}
}
