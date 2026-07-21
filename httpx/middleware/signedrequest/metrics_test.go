package signedrequest

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
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
// of the package's closed reason constants. A bare error must not leak
// into a new label series.
func TestClassifyVerifyFailure_KnownSentinels(t *testing.T) {
	cases := map[error]string{
		ErrMissingHeaders:     verifyReasonMissingHeader,
		ErrTimestampInvalid:   verifyReasonMalformedSignature,
		ErrNonceInvalid:       verifyReasonMalformedSignature,
		ErrInvalidRequest:     verifyReasonMalformedSignature,
		ErrBodyTooLarge:       verifyReasonBodyTooLarge,
		ErrNonceReplayed:      verifyReasonReplayNonce,
		ErrSignatureInvalid:   verifyReasonBadSignature,
		ErrSecretTooShort:     verifyReasonMisconfigured,
		errTimestampExpired:   verifyReasonExpired,
		errTimestampClockSkew: verifyReasonClockSkew,
	}
	for err, want := range cases {
		require.Equal(t, want, classifyVerifyFailure(err), "%v", err)
	}
	// Nil is observed (no failure) — the metric path must skip it.
	require.Empty(t, classifyVerifyFailure(nil))
}

// TestClassifyVerifyFailure_NonceStoreErrorIsNotBadSignature pins the
// fix for the mislabeling defect: a nonce-store backend failure (e.g. a
// Redis outage) is a server-side dependency failure, not a forged
// signature. Counting it as bad_signature presents an infra outage as a
// client-attributed attack spike. It must classify to the dedicated
// store_error reason instead.
func TestClassifyVerifyFailure_NonceStoreErrorIsNotBadSignature(t *testing.T) {
	wrapped := errors.New("dial tcp 10.0.0.1:6379: connect: connection refused")
	storeErr := newNonceStoreError(wrapped)

	require.Equal(t, verifyReasonStoreError, classifyVerifyFailure(storeErr))
	require.NotEqual(t, verifyReasonBadSignature, classifyVerifyFailure(storeErr))
	// The exported sentinel must be discoverable via errors.Is so
	// downstream code (and writeError) can recognise the category.
	require.ErrorIs(t, storeErr, ErrNonceStore)
	// The underlying cause must remain reachable for server-side logging.
	require.ErrorIs(t, storeErr, wrapped)
}

// TestMiddleware_NonceStoreOutage_CountsStoreErrorAndLogs proves the
// end-to-end behavior: a verified request whose nonce store is down
// returns 500, increments store_error (NOT bad_signature), and logs the
// underlying cause server-side at error level so operators have a
// diagnostic instead of a silent 500.
func TestMiddleware_NonceStoreOutage_CountsStoreErrorAndLogs(t *testing.T) {
	reg := prometheus.NewRegistry()
	metrics := NewMetrics(WithRegisterer(reg))

	now := time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC)
	outage := errors.New("redis: connection refused")

	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelError}))

	mw := Middleware(
		newResolver(t),
		failingNonceStore{err: outage},
		WithMaxClockSkew(time.Minute),
		WithClock(func() time.Time { return now }),
		WithMetrics(metrics),
		WithLogger(logger),
	)
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))

	req := signRequest(t, "POST", "/x", "", now, makeNonce("store-outage"), nil, nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	require.Equal(t, http.StatusInternalServerError, rr.Code)
	require.Equal(t, float64(1), testutil.ToFloat64(metrics.verifyFailures.WithLabelValues(verifyReasonStoreError)), "store_error")
	require.Equal(t, float64(0), testutil.ToFloat64(metrics.verifyFailures.WithLabelValues(verifyReasonBadSignature)), "bad_signature must not absorb store outages")

	logged := logBuf.String()
	require.Contains(t, logged, "level=ERROR")
	require.Contains(t, logged, outage.Error(), "underlying store error must be logged for diagnosis")
}

// TestMiddleware_ClientFailuresAreNotLoggedAsErrors proves the logger
// only fires for server-side faults: client-attributable failures (bad
// signature, missing headers) must not produce error-level log spam,
// otherwise an attacker can flood the operator's logs at will.
func TestMiddleware_ClientFailuresAreNotLoggedAsErrors(t *testing.T) {
	now := time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC)

	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelError}))

	mw := Middleware(
		newResolver(t),
		NewMemoryNonceStore(10*time.Minute),
		WithMaxClockSkew(time.Minute),
		WithClock(func() time.Time { return now }),
		WithLogger(logger),
	)
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))

	// bad_signature: valid envelope, wrong MAC.
	req := signRequest(t, "POST", "/x", "", now, makeNonce("client-badsig"), nil, nil)
	req.Header.Set(HeaderSignature, "hmac-sha256=AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	require.Equal(t, http.StatusUnauthorized, rr.Code)

	require.Empty(t, logBuf.String(), "client-attributable failures must not be logged at error level")
}

// failingNonceStore always reports a backend error, simulating a Redis
// outage during nonce observation.
type failingNonceStore struct{ err error }

func (f failingNonceStore) SeenOrStore(context.Context, string) (bool, error) {
	return false, f.err
}
