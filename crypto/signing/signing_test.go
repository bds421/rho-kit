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
var testSecret = []byte("test-webhook-secret-32bytes!!!!!")

func TestSign(t *testing.T) {
	body := []byte(`{"title":"test","message":"hello"}`)
	secret := testSecret

	sig, ts, err := Sign(body, secret)
	require.NoError(t, err)
	assert.Contains(t, sig, "sha256=")
	assert.Greater(t, ts, int64(0))

	// Same inputs at the same second produce the same signature.
	sig2, ts2, err := Sign(body, secret)
	require.NoError(t, err)
	if ts == ts2 {
		assert.Equal(t, sig, sig2)
	}

	// Different secret produces different signature.
	sig3, _, err := Sign(body, []byte("other-webhook-secret-32bytes!!!!"))
	require.NoError(t, err)
	assert.NotEqual(t, sig, sig3)

	// Different body produces different signature.
	sig4, _, err := Sign([]byte("different"), secret)
	require.NoError(t, err)
	assert.NotEqual(t, sig, sig4)
}

func TestSign_EmptySecret(t *testing.T) {
	_, _, err := Sign([]byte("body"), nil)
	assert.ErrorIs(t, err, ErrEmptySecret)

	_, _, err = Sign([]byte("body"), []byte{})
	assert.ErrorIs(t, err, ErrEmptySecret)
}

func TestVerify_RoundTrip(t *testing.T) {
	body := []byte(`{"event":"deploy","status":"success"}`)
	secret := testSecret

	sig, ts, err := Sign(body, secret)
	require.NoError(t, err)
	ok, err := Verify(secret, body, ts, sig, DefaultSignatureMaxAge)
	require.NoError(t, err)
	assert.True(t, ok, "round-trip verify should succeed")
}

func TestVerify_TamperedBody(t *testing.T) {
	body := []byte(`{"event":"deploy","status":"success"}`)
	secret := testSecret

	sig, ts, err := Sign(body, secret)
	require.NoError(t, err)
	tampered := []byte(`{"event":"deploy","status":"failure"}`)
	ok, err := Verify(secret, tampered, ts, sig, DefaultSignatureMaxAge)
	require.NoError(t, err)
	assert.False(t, ok, "tampered body should fail verification")
}

func TestVerify_WrongSecret(t *testing.T) {
	body := []byte(`{"event":"deploy"}`)
	secret := testSecret

	sig, ts, err := Sign(body, secret)
	require.NoError(t, err)
	ok, err := Verify([]byte("wrong-webhook-secret-32bytes!!!!"), body, ts, sig, DefaultSignatureMaxAge)
	require.NoError(t, err)
	assert.False(t, ok, "wrong secret should fail verification")
}

func TestVerify_WrongTimestamp(t *testing.T) {
	body := []byte(`{"event":"deploy"}`)
	secret := testSecret

	sig, ts, err := Sign(body, secret)
	require.NoError(t, err)
	ok, err := Verify(secret, body, ts+1, sig, DefaultSignatureMaxAge)
	require.NoError(t, err)
	assert.False(t, ok, "wrong timestamp should fail verification")
}

func TestVerify_InvalidSignature(t *testing.T) {
	body := []byte(`{"event":"deploy"}`)
	secret := testSecret

	_, ts, err := Sign(body, secret)
	require.NoError(t, err)
	ok, err := Verify(secret, body, ts, "sha256=invalid", DefaultSignatureMaxAge)
	require.NoError(t, err)
	assert.False(t, ok, "invalid signature should fail verification")
}

func TestVerify_ExpiredSignature(t *testing.T) {
	body := []byte(`{"event":"deploy"}`)
	secret := testSecret

	oldTimestamp := time.Now().Add(-10 * time.Minute).Unix()
	sigExpired, _ := signWithTimestamp(body, secret, oldTimestamp)
	ok, err := Verify(secret, body, oldTimestamp, sigExpired, 5*time.Minute)
	assert.ErrorIs(t, err, ErrExpiredSignature)
	assert.False(t, ok, "expired signature should fail verification")
}

func TestVerify_FutureTimestamp(t *testing.T) {
	body := []byte(`{"event":"deploy"}`)
	secret := testSecret

	futureTS := time.Now().Add(10 * time.Minute).Unix()
	sig, _ := signWithTimestamp(body, secret, futureTS)
	ok, err := Verify(secret, body, futureTS, sig, 5*time.Minute)
	assert.ErrorIs(t, err, ErrExpiredSignature)
	assert.False(t, ok, "future timestamp should fail verification")
}

func TestVerify_EmptySecret(t *testing.T) {
	_, err := Verify(nil, []byte("body"), time.Now().Unix(), "sha256=abc", DefaultSignatureMaxAge)
	assert.ErrorIs(t, err, ErrEmptySecret)

	_, err = Verify([]byte{}, []byte("body"), time.Now().Unix(), "sha256=abc", DefaultSignatureMaxAge)
	assert.ErrorIs(t, err, ErrEmptySecret)
}

func TestVerify_MissingPrefix(t *testing.T) {
	body := []byte(`{"event":"deploy"}`)
	secret := testSecret

	_, ts, err := Sign(body, secret)
	require.NoError(t, err)
	ok, err := Verify(secret, body, ts, "no-prefix", DefaultSignatureMaxAge)
	require.NoError(t, err)
	assert.False(t, ok, "signature without sha256= prefix should fail")
}

func TestSigner_WithClock_SignAndVerify(t *testing.T) {
	fixedTime := time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC)
	s := NewSigner(WithClock(func() time.Time { return fixedTime }))

	body := []byte(`{"event":"test"}`)
	secret := testSecret

	sig, ts, err := s.Sign(body, secret)
	require.NoError(t, err)
	assert.Equal(t, fixedTime.Unix(), ts)

	// Verify with the same clock succeeds.
	ok, err := s.Verify(secret, body, ts, sig, DefaultSignatureMaxAge)
	require.NoError(t, err)
	assert.True(t, ok, "round-trip with deterministic clock should succeed")
}

func TestSigner_Verify_ExpiredWithClock(t *testing.T) {
	// Sign at t=0, verify at t=10min with maxAge=5min → should fail.
	signTime := time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC)
	verifyTime := signTime.Add(10 * time.Minute)

	signSigner := NewSigner(WithClock(func() time.Time { return signTime }))
	verifySigner := NewSigner(WithClock(func() time.Time { return verifyTime }))

	body := []byte(`{"event":"test"}`)
	secret := testSecret

	sig, ts, err := signSigner.Sign(body, secret)
	require.NoError(t, err)

	ok, err := verifySigner.Verify(secret, body, ts, sig, 5*time.Minute)
	assert.ErrorIs(t, err, ErrExpiredSignature)
	assert.False(t, ok, "expired signature should fail with deterministic clock")
}

func TestSigner_Verify_FutureWithClock(t *testing.T) {
	// Sign at t=10min, verify at t=0 → future timestamp beyond skew.
	signTime := time.Date(2025, 1, 15, 12, 10, 0, 0, time.UTC)
	verifyTime := time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC)

	signSigner := NewSigner(WithClock(func() time.Time { return signTime }))
	verifySigner := NewSigner(WithClock(func() time.Time { return verifyTime }))

	body := []byte(`{"event":"test"}`)
	secret := testSecret

	sig, ts, err := signSigner.Sign(body, secret)
	require.NoError(t, err)

	ok, err := verifySigner.Verify(secret, body, ts, sig, 5*time.Minute)
	assert.ErrorIs(t, err, ErrExpiredSignature)
	assert.False(t, ok, "future timestamp beyond skew should fail")
}

func TestSigner_Sign_EmptySecret(t *testing.T) {
	s := NewSigner()
	_, _, err := s.Sign([]byte("body"), nil)
	assert.ErrorIs(t, err, ErrEmptySecret)
}

func TestSigner_Verify_EmptySecret(t *testing.T) {
	s := NewSigner()
	_, err := s.Verify(nil, []byte("body"), time.Now().Unix(), "sha256=abc", DefaultSignatureMaxAge)
	assert.ErrorIs(t, err, ErrEmptySecret)
}

// signWithTimestamp is a test helper that signs with an explicit timestamp.
func signWithTimestamp(body []byte, secret []byte, timestamp int64) (string, int64) {
	payload := fmt.Appendf(nil, "%d.", timestamp)
	payload = append(payload, body...)
	mac := hmac.New(sha256.New, secret)
	mac.Write(payload)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil)), timestamp
}
