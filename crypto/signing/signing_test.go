package signing

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testSecret is a 32-byte key for test use (matches minSecretLen).
var testSecret = Secret("test-webhook-secret-32bytes!!!!!")

func TestNewSecretCopiesInput(t *testing.T) {
	raw := []byte("test-webhook-secret-32bytes!!!!!")
	secret := NewSecret(raw)
	raw[0] = 'X'

	if string(secret) != "test-webhook-secret-32bytes!!!!!" {
		t.Fatalf("NewSecret retained caller-owned bytes: %q", string(secret))
	}
}

func TestSign(t *testing.T) {
	body := []byte(`{"title":"test","message":"hello"}`)
	secret := testSecret

	sig, ts, err := Sign(secret, body)
	require.NoError(t, err)
	assert.Contains(t, sig, "sha256=")
	assert.Greater(t, ts, int64(0))

	// Same inputs at the same second produce the same signature.
	sig2, ts2, err := Sign(secret, body)
	require.NoError(t, err)
	if ts == ts2 {
		assert.Equal(t, sig, sig2)
	}

	// Different secret produces different signature.
	sig3, _, err := Sign(Secret("other-webhook-secret-32bytes!!!!"), body)
	require.NoError(t, err)
	assert.NotEqual(t, sig, sig3)

	// Different body produces different signature.
	sig4, _, err := Sign(secret, []byte("different"))
	require.NoError(t, err)
	assert.NotEqual(t, sig, sig4)
}

func TestSign_EmptySecret(t *testing.T) {
	_, _, err := Sign(nil, []byte("body"))
	assert.ErrorIs(t, err, ErrEmptySecret)

	_, _, err = Sign(Secret{}, []byte("body"))
	assert.ErrorIs(t, err, ErrEmptySecret)
}

func TestVerify_RoundTrip(t *testing.T) {
	body := []byte(`{"event":"deploy","status":"success"}`)
	secret := testSecret

	sig, ts, err := Sign(secret, body)
	require.NoError(t, err)
	assert.NoError(t, Verify(secret, body, ts, sig, DefaultSignatureMaxAge),
		"round-trip verify should succeed")
}

func TestVerify_TamperedBody(t *testing.T) {
	body := []byte(`{"event":"deploy","status":"success"}`)
	secret := testSecret

	sig, ts, err := Sign(secret, body)
	require.NoError(t, err)
	tampered := []byte(`{"event":"deploy","status":"failure"}`)
	assert.ErrorIs(t, Verify(secret, tampered, ts, sig, DefaultSignatureMaxAge), ErrInvalidSignature,
		"tampered body should fail verification")
}

func TestVerify_WrongSecret(t *testing.T) {
	body := []byte(`{"event":"deploy"}`)
	secret := testSecret

	sig, ts, err := Sign(secret, body)
	require.NoError(t, err)
	assert.ErrorIs(t,
		Verify(Secret("wrong-webhook-secret-32bytes!!!!"), body, ts, sig, DefaultSignatureMaxAge),
		ErrInvalidSignature, "wrong secret should fail verification")
}

func TestVerify_WrongTimestamp(t *testing.T) {
	body := []byte(`{"event":"deploy"}`)
	secret := testSecret

	sig, ts, err := Sign(secret, body)
	require.NoError(t, err)
	assert.ErrorIs(t, Verify(secret, body, ts+1, sig, DefaultSignatureMaxAge), ErrInvalidSignature,
		"wrong timestamp should fail verification")
}

func TestVerify_InvalidSignature(t *testing.T) {
	body := []byte(`{"event":"deploy"}`)
	secret := testSecret

	_, ts, err := Sign(secret, body)
	require.NoError(t, err)
	assert.ErrorIs(t, Verify(secret, body, ts, "sha256=invalid", DefaultSignatureMaxAge), ErrInvalidSignature,
		"invalid signature should fail verification")
}

func TestVerify_ExpiredSignature(t *testing.T) {
	body := []byte(`{"event":"deploy"}`)
	secret := testSecret

	oldTimestamp := time.Now().Add(-10 * time.Minute).Unix()
	sigExpired, _ := signWithTimestamp(body, secret, oldTimestamp)
	assert.ErrorIs(t,
		Verify(secret, body, oldTimestamp, sigExpired, 5*time.Minute), ErrSignatureExpired,
		"expired signature should fail verification")
}

func TestVerify_FutureTimestamp(t *testing.T) {
	body := []byte(`{"event":"deploy"}`)
	secret := testSecret

	futureTS := time.Now().Add(10 * time.Minute).Unix()
	sig, _ := signWithTimestamp(body, secret, futureTS)
	assert.ErrorIs(t,
		Verify(secret, body, futureTS, sig, 5*time.Minute), ErrSignatureClockSkew,
		"future timestamp should fail verification")
}

func TestVerify_EmptySecret(t *testing.T) {
	assert.ErrorIs(t, Verify(nil, []byte("body"), time.Now().Unix(), "sha256=abc", DefaultSignatureMaxAge), ErrEmptySecret)
	assert.ErrorIs(t, Verify(Secret{}, []byte("body"), time.Now().Unix(), "sha256=abc", DefaultSignatureMaxAge), ErrEmptySecret)
}

func TestVerify_MissingPrefix(t *testing.T) {
	body := []byte(`{"event":"deploy"}`)
	secret := testSecret

	_, ts, err := Sign(secret, body)
	require.NoError(t, err)
	assert.ErrorIs(t, Verify(secret, body, ts, "no-prefix", DefaultSignatureMaxAge), ErrInvalidSignature,
		"signature without sha256= prefix should fail")
}

func TestSigner_WithClock_SignAndVerify(t *testing.T) {
	fixedTime := time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC)
	s := NewSigner(WithClock(func() time.Time { return fixedTime }))

	body := []byte(`{"event":"test"}`)
	secret := testSecret

	sig, ts, err := s.Sign(secret, body)
	require.NoError(t, err)
	assert.Equal(t, fixedTime.Unix(), ts)

	// Verify with the same clock succeeds.
	assert.NoError(t, s.Verify(secret, body, ts, sig, DefaultSignatureMaxAge),
		"round-trip with deterministic clock should succeed")
}

func TestSigner_Verify_ExpiredWithClock(t *testing.T) {
	// Sign at t=0, verify at t=10min with maxAge=5min → should fail.
	signTime := time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC)
	verifyTime := signTime.Add(10 * time.Minute)

	signSigner := NewSigner(WithClock(func() time.Time { return signTime }))
	verifySigner := NewSigner(WithClock(func() time.Time { return verifyTime }))

	body := []byte(`{"event":"test"}`)
	secret := testSecret

	sig, ts, err := signSigner.Sign(secret, body)
	require.NoError(t, err)

	err = verifySigner.Verify(secret, body, ts, sig, 5*time.Minute)
	assert.ErrorIs(t, err, ErrSignatureExpired, "expired signature should fail with deterministic clock")
}

func TestSigner_Verify_FutureWithClock(t *testing.T) {
	// Sign at t=10min, verify at t=0 → future timestamp beyond skew.
	signTime := time.Date(2025, 1, 15, 12, 10, 0, 0, time.UTC)
	verifyTime := time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC)

	signSigner := NewSigner(WithClock(func() time.Time { return signTime }))
	verifySigner := NewSigner(WithClock(func() time.Time { return verifyTime }))

	body := []byte(`{"event":"test"}`)
	secret := testSecret

	sig, ts, err := signSigner.Sign(secret, body)
	require.NoError(t, err)

	err = verifySigner.Verify(secret, body, ts, sig, 5*time.Minute)
	assert.ErrorIs(t, err, ErrSignatureClockSkew, "future timestamp beyond skew should fail")
}

func TestSigner_WithFutureSkew_RejectsBeyondLimit(t *testing.T) {
	verifierClock := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	signerClock := verifierClock.Add(2 * time.Minute)

	signer := NewSigner(WithClock(func() time.Time { return signerClock }))
	verifier := NewSigner(
		WithClock(func() time.Time { return verifierClock }),
		WithFutureSkew(1*time.Minute),
	)

	secret := Secret("secret-secret-secret-secret-32by")
	sig, ts, err := signer.Sign(secret, []byte("body"))
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}

	err = verifier.Verify(secret, []byte("body"), ts, sig, 5*time.Minute)
	assert.ErrorIs(t, err, ErrSignatureClockSkew)
}

func TestSigner_WithFutureSkew_AcceptsWithinLimit(t *testing.T) {
	verifierClock := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	signerClock := verifierClock.Add(45 * time.Second)

	signer := NewSigner(WithClock(func() time.Time { return signerClock }))
	verifier := NewSigner(
		WithClock(func() time.Time { return verifierClock }),
		WithFutureSkew(1*time.Minute),
	)

	secret := Secret("secret-secret-secret-secret-32by")
	sig, ts, err := signer.Sign(secret, []byte("body"))
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}

	err = verifier.Verify(secret, []byte("body"), ts, sig, 5*time.Minute)
	assert.NoError(t, err)
}

func TestSignerOptions_PanicOnInvalidInput(t *testing.T) {
	assert.Panics(t, func() { WithFutureSkew(-time.Second) })
	assert.Panics(t, func() { NewSigner(nil) })
}

func TestSigner_Sign_EmptySecret(t *testing.T) {
	s := NewSigner()
	_, _, err := s.Sign(nil, []byte("body"))
	assert.ErrorIs(t, err, ErrEmptySecret)
}

func TestSigner_Verify_EmptySecret(t *testing.T) {
	s := NewSigner()
	err := s.Verify(nil, []byte("body"), time.Now().Unix(), "sha256=abc", DefaultSignatureMaxAge)
	assert.ErrorIs(t, err, ErrEmptySecret)
}

func TestSigner_ZeroValueReturnsInvalidSigner(t *testing.T) {
	var s Signer

	_, _, err := s.Sign(testSecret, []byte("body"))
	assert.ErrorIs(t, err, ErrInvalidSigner)

	err = s.Verify(testSecret, []byte("body"), time.Now().Unix(), "sha256=abc", DefaultSignatureMaxAge)
	assert.ErrorIs(t, err, ErrInvalidSigner)
}

func TestSigner_NilReceiverReturnsInvalidSigner(t *testing.T) {
	var s *Signer

	_, _, err := s.Sign(testSecret, []byte("body"))
	assert.ErrorIs(t, err, ErrInvalidSigner)

	err = s.Verify(testSecret, []byte("body"), time.Now().Unix(), "sha256=abc", DefaultSignatureMaxAge)
	assert.ErrorIs(t, err, ErrInvalidSigner)
}

func TestVerify_RejectsNonPositiveMaxAge(t *testing.T) {
	s := NewSigner()
	sig, ts, err := s.Sign(testSecret, []byte("body"))
	require.NoError(t, err)

	err = s.Verify(testSecret, []byte("body"), ts, sig, 0)
	assert.ErrorIs(t, err, ErrInvalidMaxAge)

	err = s.Verify(testSecret, []byte("body"), ts, sig, -time.Second)
	assert.ErrorIs(t, err, ErrInvalidMaxAge)
}

func TestSignContext_RejectsAmbiguousContextSeparators(t *testing.T) {
	s := NewSigner()
	cases := []CanonicalContext{
		{Method: "POST\nGET", Path: "/hooks", Domain: "webhook"},
		{Method: "POST", Path: "/hooks\r\nX", Domain: "webhook"},
		{Method: "POST", Path: "/hooks", Domain: "webhook\nv2"},
	}

	for _, ctx := range cases {
		_, _, err := s.SignContext(ctx, testSecret, []byte("body"))
		assert.ErrorIs(t, err, ErrInvalidContext)
	}
}

func TestVerifyContext_RejectsAmbiguousContextSeparators(t *testing.T) {
	s := NewSigner()
	ctx := CanonicalContext{Method: "POST", Path: "/hooks", Domain: "webhook"}
	sig, ts, err := s.SignContext(ctx, testSecret, []byte("body"))
	require.NoError(t, err)

	err = s.VerifyContext(CanonicalContext{Method: "POST\nGET", Path: "/hooks", Domain: "webhook"}, testSecret, []byte("body"), ts, sig, DefaultSignatureMaxAge)
	assert.ErrorIs(t, err, ErrInvalidContext)
}

func TestWithClock_PanicsOnNil(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on nil clock")
		}
	}()
	_ = WithClock(nil)
}

func TestVerify_FutureTimestampReturnsClockSkew(t *testing.T) {
	// Spec contract: signing with a timestamp beyond now+maxAge must
	// return ErrSignatureClockSkew so operators can alert separately on
	// "producer clock is wrong" vs "consumer is slow / replay window
	// missed".
	body := []byte(`{"event":"deploy"}`)
	maxAge := 5 * time.Minute

	verifier := NewSigner()
	// Skew the signer two maxAge windows into the future relative to
	// the verifier — this is well beyond the default futureSkew so the
	// future-side guard must trip.
	signClock := time.Now().Add(2 * maxAge)
	signer := NewSigner(WithClock(func() time.Time { return signClock }))

	sig, ts, err := signer.Sign(testSecret, body)
	require.NoError(t, err)

	verifyErr := verifier.Verify(testSecret, body, ts, sig, maxAge)
	assert.ErrorIs(t, verifyErr, ErrSignatureClockSkew)
	assert.NotErrorIs(t, verifyErr, ErrSignatureExpired)
}

func TestVerify_PastMaxAgeReturnsExpired(t *testing.T) {
	// Spec contract: signing with a timestamp older than maxAge must
	// return ErrSignatureExpired (not ErrSignatureClockSkew). Tests
	// the "past" branch of the renamed sentinel.
	body := []byte(`{"event":"deploy"}`)
	maxAge := 5 * time.Minute

	verifier := NewSigner()
	signClock := time.Now().Add(-2 * maxAge)
	signer := NewSigner(WithClock(func() time.Time { return signClock }))

	sig, ts, err := signer.Sign(testSecret, body)
	require.NoError(t, err)

	verifyErr := verifier.Verify(testSecret, body, ts, sig, maxAge)
	assert.ErrorIs(t, verifyErr, ErrSignatureExpired)
	assert.NotErrorIs(t, verifyErr, ErrSignatureClockSkew)
}

// signWithTimestamp is a test helper that signs with an explicit timestamp.
func signWithTimestamp(body []byte, secret Secret, timestamp int64) (string, int64) {
	payload := fmt.Appendf(nil, "%d.", timestamp)
	payload = append(payload, body...)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil)), timestamp
}
