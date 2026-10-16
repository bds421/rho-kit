package jwtutil

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lestrrat-go/jwx/v3/jwa"
)

type signingStringRecorderFunc func(context.Context, string, string, time.Time) error

func (f signingStringRecorderFunc) RecordIssued(ctx context.Context, issuer, jti string, expires time.Time) error {
	return f(ctx, issuer, jti, expires)
}

func TestSigningStringClaimsRoundTripAndDefaultIsolation(t *testing.T) {
	key := mustECDSAKey(t)
	names := []string{"sid", "sv"}
	option := WithSigningStringClaims(names...)
	names[0] = "iss" // option already copied its construction-time policy
	recorder := new(recorderStub)
	p, err := NewSigningProvider(t.Context(), staticRotator(key), WithSigningRotationInterval(time.Hour),
		WithSigningExpectedIssuer("invented-socket-issuer"), WithSigningExpectedAudience("invented-socket-audience"),
		WithSigningDefaultLifetime(time.Minute), option, WithIssuedJTIRecorder(recorder))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })
	ks := verifierForKey(t, key.Public(), jwa.ES256())
	verify := NewProviderWithKeySet(ks, WithExpectedIssuer("invented-socket-issuer"),
		WithExpectedAudience("invented-socket-audience"), WithStringClaims("sid", "sv"))
	fields := map[string]string{"sid": "11111111-1111-4111-8111-111111111111", "sv": "9007199254740993"}
	token, err := p.SignWithStringClaims(t.Context(), Claims{Subject: "invented-grant", Issuer: "caller-cannot-override",
		Permissions: []string{"invented:view"}, Scopes: "invented:read"}, fields)
	if err != nil {
		t.Fatal(err)
	}
	fields["sid"] = "after-return-mutation"
	c, err := verify.VerifyContext(t.Context(), token, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	sid, ok := c.StringClaim("sid")
	sv, versionOK := c.StringClaim("sv")
	if !ok || !versionOK || sid != "11111111-1111-4111-8111-111111111111" || sv != "9007199254740993" ||
		c.Issuer != "invented-socket-issuer" || c.Subject != "invented-grant" || c.ID == "" || c.Scopes != "invented:read" || len(c.Permissions) != 1 {
		t.Fatal("additional strings or maintained pinned claims changed")
	}
	old, err := p.SignContext(t.Context(), *c)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := verify.VerifyContext(t.Context(), old, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, present := plain.StringClaim("sid"); present {
		t.Fatal("legacy signing implicitly forwarded verified claims")
	}
	if len(recorder.snapshot()) != 2 {
		t.Fatal("maintained issued-JTI recorder skipped")
	}
	for _, bad := range []map[string]string{
		{"iss": "override"}, {"unknown": "value"}, {"sid": ""}, {"sid": "control\n"},
		{"sid": string([]byte{0xff})}, {"sid": strings.Repeat("x", 2049)},
	} {
		if _, err := p.SignWithStringClaims(t.Context(), Claims{Subject: "invented-grant"}, bad); !errors.Is(err, ErrSigningStringClaims) {
			t.Fatal("invalid claims allowed", err)
		}
	}
	if len(recorder.snapshot()) != 2 {
		t.Fatal("invalid fields reached issuance ledger")
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := p.SignWithStringClaims(t.Context(), Claims{Subject: "invented-grant"}, nil); !errors.Is(err, ErrSigningProviderClosed) {
		t.Fatal("new API bypassed maintained shutdown", err)
	}
}

func TestSigningStringClaimPolicyRejectsReservedNamesAndSizes(t *testing.T) {
	for _, name := range []string{"iss", "ISS", "aud", "sub", "jti", "iat", "exp", "nbf", "permissions", "scopes", "scope", "alg", "kid", "typ", "jku", "x5u", "", "1bad", "bad-name", strings.Repeat("a", 65)} {
		t.Run("reserved-or-malformed", func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("unsafe allowlist accepted")
				}
			}()
			WithSigningStringClaims(name)
		})
	}
	p := &SigningProvider{}
	if _, err := p.copySigningStringClaims(map[string]string{"sid": "value"}); !errors.Is(err, ErrSigningStringClaims) {
		t.Fatal("default empty allowlist accepted fields")
	}
	WithSigningStringClaims("a", "b", "c", "d", "e")(p)
	if _, err := p.copySigningStringClaims(map[string]string{"a": strings.Repeat("x", 2048), "b": strings.Repeat("x", 2048), "c": strings.Repeat("x", 2048), "d": strings.Repeat("x", 2048)}); !errors.Is(err, ErrSigningStringClaims) {
		t.Fatal("total byte limit not enforced")
	}
}

func TestSigningStringClaimsCopiedBeforeRecorderAndBoundToVerifierSize(t *testing.T) {
	key := mustECDSAKey(t)
	entered, proceed := make(chan struct{}), make(chan struct{})
	p, err := NewSigningProvider(t.Context(), staticRotator(key), WithSigningRotationInterval(time.Hour),
		WithSigningExpectedIssuer("invented"), WithSigningExpectedAudience("invented"), WithSigningDefaultLifetime(time.Minute),
		WithSigningStringClaims("sid", "a", "b", "c", "d"), WithIssuedJTIRecorder(signingStringRecorderFunc(func(context.Context, string, string, time.Time) error {
			close(entered)
			<-proceed
			return nil
		})))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })
	fields := map[string]string{"sid": "original"}
	result := make(chan string, 1)
	var wg sync.WaitGroup
	wg.Go(func() {
		token, err := p.SignWithStringClaims(t.Context(), Claims{Subject: "invented"}, fields)
		if err != nil {
			t.Error(err)
		}
		result <- token
	})
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("recorder not reached")
	}
	fields["sid"] = "after-snapshot"
	close(proceed)
	wg.Wait()
	provider := NewProviderWithKeySet(verifierForKey(t, key.Public(), jwa.ES256()), WithExpectedIssuer("invented"), WithExpectedAudience("invented"), WithStringClaims("sid"))
	c, err := provider.VerifyContext(t.Context(), <-result, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if sid, _ := c.StringClaim("sid"); sid != "original" {
		t.Fatal("caller map mutation changed issued token")
	}
	// Escaping expands otherwise-bounded fields past the verifier's token cap.
	big := map[string]string{"a": strings.Repeat(`"`, 2000), "b": strings.Repeat(`"`, 2000), "c": strings.Repeat(`"`, 2000), "d": strings.Repeat(`"`, 2000)}
	if _, err := p.SignWithStringClaims(t.Context(), Claims{Subject: "invented"}, big); !errors.Is(err, ErrSigningStringClaims) {
		t.Fatal("issuer emitted extras beyond maintained verifier token limit", err)
	}
}

func TestSigningStringClaimsRetainsKeyRotationStaleAndMissingGuards(t *testing.T) {
	first, second := mustECDSAKey(t), mustECDSAKey(t)
	p, err := NewSigningProvider(t.Context(), staticRotator(first), WithSigningRotationInterval(time.Hour),
		WithSigningExpectedIssuer("invented"), WithSigningExpectedAudience("invented"), WithSigningDefaultLifetime(time.Minute), WithSigningStringClaims("sid"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })
	fields := map[string]string{"sid": "original"}
	old, err := p.SignWithStringClaims(t.Context(), Claims{Subject: "invented"}, fields)
	if err != nil {
		t.Fatal(err)
	}
	oldVerifier := NewProviderWithKeySet(verifierForKey(t, first.Public(), jwa.ES256()), WithExpectedIssuer("invented"), WithExpectedAudience("invented"), WithStringClaims("sid"))
	if _, err := oldVerifier.VerifyContext(t.Context(), old, time.Now()); err != nil {
		t.Fatal(err)
	}
	p.src = staticRotator(second) // no refresh tick can race this hour-long fixture
	if err := p.refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	rotated, err := p.SignWithStringClaims(t.Context(), Claims{Subject: "invented"}, fields)
	if err != nil {
		t.Fatal(err)
	}
	newVerifier := NewProviderWithKeySet(verifierForKey(t, second.Public(), jwa.ES256()), WithExpectedIssuer("invented"), WithExpectedAudience("invented"), WithStringClaims("sid"))
	c, err := newVerifier.VerifyContext(t.Context(), rotated, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if value, _ := c.StringClaim("sid"); value != "original" {
		t.Fatal("rotation dropped custom binding")
	}
	if _, err := oldVerifier.VerifyContext(t.Context(), rotated, time.Now()); err == nil {
		t.Fatal("rotation failed to switch maintained key/kid")
	}
	p.lastSuccessfulRefresh.Store(time.Now().Add(-p.maxStale - time.Second).UnixNano())
	if _, err := p.SignWithStringClaims(t.Context(), Claims{Subject: "invented"}, fields); !errors.Is(err, ErrSigningKeyUnavailable) {
		t.Fatal("stale key bypass", err)
	}
	p.current.Store(nil)
	if _, err := p.SignWithStringClaims(t.Context(), Claims{Subject: "invented"}, fields); !errors.Is(err, ErrSigningKeyUnavailable) {
		t.Fatal("missing key bypass", err)
	}
}
