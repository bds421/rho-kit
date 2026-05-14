package signedrequest

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

func TestSignedRequestMetrics_ReusesCollectors(t *testing.T) {
	reg := prometheus.NewRegistry()
	m1 := NewMetrics(WithRegisterer(reg))
	m2 := NewMetrics(WithRegisterer(reg))

	if m1.verifyFailures != m2.verifyFailures {
		t.Fatal("NewMetrics should reuse verifyFailures collector on duplicate registration")
	}
}

func TestSignedRequestMetrics_PanicOnNil(t *testing.T) {
	require.Panics(t, func() { WithMetrics(nil) })
	require.Panics(t, func() { WithRegisterer(nil) })
	require.Panics(t, func() { NewMetrics(nil) })
}

// TestSignedRequestMetrics_CountsByReason exercises one request per
// reason label and asserts the matching counter increments. The
// closed reason set is part of the dashboard contract; if a future
// change collapses or adds a reason, this test will break loudly so
// operators do not get silently-dropped categories.
func TestSignedRequestMetrics_CountsByReason(t *testing.T) {
	reg := prometheus.NewRegistry()
	metrics := NewMetrics(WithRegisterer(reg))

	store := NewMemoryNonceStore(10 * time.Minute)
	// Pin the verifier clock so future-skew vs past-expiry are
	// deterministic.
	now := time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC)
	mw := Middleware(
		newResolver(t),
		store,
		WithMaxClockSkew(time.Minute),
		WithClock(func() time.Time { return now }),
		WithMetrics(metrics),
	)
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))

	// missing_header: omit X-Signature-Timestamp.
	{
		req := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(""))
		req.Header.Set(HeaderNonce, makeNonce("missing"))
		req.Header.Set(HeaderKeyID, keyID)
		req.Header.Set(HeaderSignature, "hmac-sha256=AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		require.Equal(t, http.StatusBadRequest, rr.Code)
	}

	// malformed_signature: bad timestamp value -> ErrTimestampInvalid
	// (not a skew direction).
	{
		req := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(""))
		req.Header.Set(HeaderTimestamp, "not-a-number")
		req.Header.Set(HeaderNonce, makeNonce("malformed"))
		req.Header.Set(HeaderKeyID, keyID)
		req.Header.Set(HeaderSignature, "hmac-sha256=AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		require.Equal(t, http.StatusBadRequest, rr.Code)
	}

	// expired: timestamp older than maxClockSkew window.
	{
		past := now.Add(-time.Hour)
		req := signRequest(t, "POST", "/x", "", past, makeNonce("expired"), nil, nil)
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		require.Equal(t, http.StatusBadRequest, rr.Code)
	}

	// clock_skew: timestamp too far in the future.
	{
		future := now.Add(time.Hour)
		req := signRequest(t, "POST", "/x", "", future, makeNonce("future"), nil, nil)
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		require.Equal(t, http.StatusBadRequest, rr.Code)
	}

	// bad_signature: valid envelope but the MAC will not verify because
	// we overwrite X-Signature with a constant.
	{
		req := signRequest(t, "POST", "/x", "", now, makeNonce("bad-sig"), nil, nil)
		req.Header.Set(HeaderSignature, "hmac-sha256=AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		require.Equal(t, http.StatusUnauthorized, rr.Code)
	}

	// replay_nonce: send the same valid request twice; the second
	// hits the nonce store.
	{
		nonce := makeNonce("replay")
		r1 := signRequest(t, "POST", "/x", "", now, nonce, nil, nil)
		rr1 := httptest.NewRecorder()
		h.ServeHTTP(rr1, r1)
		require.Equal(t, http.StatusOK, rr1.Code)

		r2 := signRequest(t, "POST", "/x", "", now, nonce, nil, nil)
		rr2 := httptest.NewRecorder()
		h.ServeHTTP(rr2, r2)
		require.Equal(t, http.StatusUnauthorized, rr2.Code)
	}

	require.Equal(t, float64(1), testutil.ToFloat64(metrics.verifyFailures.WithLabelValues(verifyReasonMissingHeader)), "missing_header")
	require.Equal(t, float64(1), testutil.ToFloat64(metrics.verifyFailures.WithLabelValues(verifyReasonMalformedSignature)), "malformed_signature")
	require.Equal(t, float64(1), testutil.ToFloat64(metrics.verifyFailures.WithLabelValues(verifyReasonExpired)), "expired")
	require.Equal(t, float64(1), testutil.ToFloat64(metrics.verifyFailures.WithLabelValues(verifyReasonClockSkew)), "clock_skew")
	require.Equal(t, float64(1), testutil.ToFloat64(metrics.verifyFailures.WithLabelValues(verifyReasonBadSignature)), "bad_signature")
	require.Equal(t, float64(1), testutil.ToFloat64(metrics.verifyFailures.WithLabelValues(verifyReasonReplayNonce)), "replay_nonce")
}

// TestClassifyVerifyFailure_KnownSentinels guards the bounded
// reason-label set: every documented sentinel must classify to one
// of the package's six reason constants. A bare error must not leak
// into a new label series.
func TestClassifyVerifyFailure_KnownSentinels(t *testing.T) {
	cases := map[error]string{
		ErrMissingHeaders:     verifyReasonMissingHeader,
		ErrTimestampInvalid:   verifyReasonMalformedSignature,
		ErrNonceInvalid:       verifyReasonMalformedSignature,
		ErrInvalidRequest:     verifyReasonMalformedSignature,
		ErrBodyTooLarge:       verifyReasonMalformedSignature,
		ErrNonceReplayed:      verifyReasonReplayNonce,
		ErrSignatureInvalid:   verifyReasonBadSignature,
		ErrSecretTooShort:     verifyReasonBadSignature,
		errTimestampExpired:   verifyReasonExpired,
		errTimestampClockSkew: verifyReasonClockSkew,
	}
	for err, want := range cases {
		require.Equal(t, want, classifyVerifyFailure(err), "%v", err)
	}
	// Nil is observed (no failure) — the metric path must skip it.
	require.Empty(t, classifyVerifyFailure(nil))
}

