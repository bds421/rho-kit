package app

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestWebhookHandler_SignedRequestAccepted exercises the canonical
// happy path: HMAC-signed POST with an idempotency key is verified,
// decoded, and recorded.
func TestWebhookHandler_SignedRequestAccepted(t *testing.T) {
	hmacKey := make([]byte, 32)
	for i := range hmacKey {
		hmacKey[i] = byte(i)
	}
	rec := newReceiver()
	handler := buildWebhookHandler(hmacKey, rec)

	srv := httptest.NewServer(handler)
	defer srv.Close()

	body := []byte(`{"id":"evt-1","kind":"order.created","payload":"{}"}`)
	req := newSignedRequest(t, srv.URL+"/", "POST", hmacKey, body, 1)
	req.Header.Set("Idempotency-Key", "evt-1")

	resp, err := srv.Client().Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusAccepted {
		bodyBytes, _ := readAll(resp.Body)
		t.Fatalf("expected 202, got %d. body=%q", resp.StatusCode, string(bodyBytes))
	}
	require.Len(t, rec.events, 1)
	assert.Equal(t, "evt-1", rec.events[0].ID)
	assert.Equal(t, "order.created", rec.events[0].Kind)
}

// TestWebhookHandler_UnsignedRejected verifies the verify-first
// wiring: an unsigned request must not reach the idempotency cache
// (and therefore cannot poison it).
func TestWebhookHandler_UnsignedRejected(t *testing.T) {
	hmacKey := make([]byte, 32)
	rec := newReceiver()
	handler := buildWebhookHandler(hmacKey, rec)
	srv := httptest.NewServer(handler)
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/", "application/json", strings.NewReader(`{"id":"x"}`))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	assert.GreaterOrEqual(t, resp.StatusCode, 400)
	assert.Less(t, resp.StatusCode, 500)
	assert.Empty(t, rec.events, "verification must reject before handler")
}

// TestWebhookHandler_IdempotentRetry verifies that a verified retry
// with the same Idempotency-Key returns the cached 202 instead of
// re-recording the event.
func TestWebhookHandler_IdempotentRetry(t *testing.T) {
	hmacKey := make([]byte, 32)
	for i := range hmacKey {
		hmacKey[i] = byte(i + 0x42)
	}
	rec := newReceiver()
	handler := buildWebhookHandler(hmacKey, rec)
	srv := httptest.NewServer(handler)
	defer srv.Close()

	body := []byte(`{"id":"evt-2","kind":"order.created","payload":"{}"}`)

	send := func(t *testing.T, nonceSeed byte) {
		t.Helper()
		req := newSignedRequest(t, srv.URL+"/", "POST", hmacKey, body, nonceSeed)
		req.Header.Set("Idempotency-Key", "retry-token-001")
		resp, err := srv.Client().Do(req)
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusAccepted {
			bodyBytes, _ := readAll(resp.Body)
			t.Fatalf("expected 202, got %d. body=%q", resp.StatusCode, string(bodyBytes))
		}
	}
	send(t, 1)
	send(t, 2)
	assert.Len(t, rec.events, 1, "idempotent retry must not double-record")
}

// newSignedRequest builds an http.Request signed with the canonical
// kit scheme (X-Signature-Timestamp / -Nonce / -Key-Id / -Signature)
// so the smoke test mirrors what a real webhook publisher would
// send. The kit's documented header constants are exported by the
// signedrequest package; we re-derive the canonical string here to
// keep the test self-contained. The nonceSeed argument distinguishes
// otherwise-identical retries — a real publisher would draw a fresh
// random nonce per attempt.
func newSignedRequest(t *testing.T, url, method string, key, body []byte, nonceSeed byte) *http.Request {
	t.Helper()
	req, err := http.NewRequest(method, url, bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")

	ts := strconv.FormatInt(time.Now().Unix(), 10)
	nonceBytes := make([]byte, 16)
	for i := range nonceBytes {
		nonceBytes[i] = byte(i+1) ^ nonceSeed
	}
	nonce := base64.RawURLEncoding.EncodeToString(nonceBytes)
	keyID := "demo-tenant"

	bodyHash := sha256.Sum256(body)
	// The kit lowercases the canonical host (see signedrequest.canonicalRequestHost).
	canonical := strings.Join([]string{
		method,
		req.URL.RequestURI(),
		strings.ToLower(req.URL.Host),
		"application/json",
		ts,
		nonce,
		hex.EncodeToString(bodyHash[:]),
	}, "\n")

	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(canonical))
	// The kit decodes signatures with base64.StdEncoding (padded);
	// see signedrequest.decodeSignatureMAC.
	sig := base64.StdEncoding.EncodeToString(mac.Sum(nil))

	req.Header.Set("X-Signature-Timestamp", ts)
	req.Header.Set("X-Signature-Nonce", nonce)
	req.Header.Set("X-Signature-Key-Id", keyID)
	req.Header.Set("X-Signature", "hmac-sha256="+sig)
	return req
}

// TestListenPortHint covers the helper used in operator logging.
func TestListenPortHint(t *testing.T) {
	p, err := listenPortHint(":8090")
	require.NoError(t, err)
	assert.Equal(t, 8090, p)
}

// Compile-time check that recordedEvent is JSON-encodable; if the
// type ever grows a non-encodable field this test fails fast.
func TestRecordedEventEncodes(t *testing.T) {
	buf, err := json.Marshal(recordedEvent{ID: "x"})
	require.NoError(t, err)
	assert.Contains(t, string(buf), `"id":"x"`)
}

// Smoke probe for the listHandler — returns JSON array.
func TestListHandler_ReturnsEvents(t *testing.T) {
	r := newReceiver()
	r.events = []recordedEvent{{ID: "a"}, {ID: "b"}}
	w := httptest.NewRecorder()
	r.listHandler()(w, httptest.NewRequest(http.MethodGet, "/received", nil))
	resp := w.Result()
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	body, _ := readAll(resp.Body)
	assert.Contains(t, string(body), `"id":"a"`)
	assert.Contains(t, string(body), `"id":"b"`)
}

func readAll(r interface{ Read([]byte) (int, error) }) ([]byte, error) {
	var buf bytes.Buffer
	tmp := make([]byte, 1024)
	for {
		n, err := r.Read(tmp)
		if n > 0 {
			buf.Write(tmp[:n])
		}
		if err != nil {
			if err.Error() == "EOF" {
				return buf.Bytes(), nil
			}
			return buf.Bytes(), err
		}
	}
}

// Sanity: the canonical signature scheme uses these exact header
// names — drift here means a kit BC break.
var _ = fmt.Sprintf // keep imports tidy across the file
