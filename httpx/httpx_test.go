package httpx

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"golang.org/x/net/http2"

	"github.com/bds421/rho-kit/core/v2/apperror"
)

// --- WriteJSON ---

func TestWriteJSON_Success(t *testing.T) {
	rec := httptest.NewRecorder()
	if err := WriteJSON(rec, nil, http.StatusOK, map[string]string{"key": "value"}); err != nil {
		t.Fatalf("WriteJSON: %v", err)
	}

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("expected application/json, got %s", ct)
	}
	var got map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got["key"] != "value" {
		t.Fatalf("expected key=value, got %v", got)
	}
}

func TestWriteJSON_MarshalError(t *testing.T) {
	rec := httptest.NewRecorder()
	// Channels cannot be marshaled to JSON.
	err := WriteJSON(rec, nil, http.StatusOK, make(chan int))
	if err == nil {
		t.Fatalf("expected marshal error, got nil")
	}

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rec.Code)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
	if !strings.Contains(rec.Body.String(), "internal error") {
		t.Fatalf("expected internal error in body, got: %s", rec.Body.String())
	}
}

func TestWriteJSON_MarshalError_LogsViaRequestLogger(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req = req.WithContext(SetLogger(req.Context(), logger))

	rec := httptest.NewRecorder()
	// Channels cannot be marshaled to JSON.
	err := WriteJSON(rec, req, http.StatusOK, make(chan int))
	if err == nil {
		t.Fatal("expected marshal error, got nil")
	}
	if !strings.Contains(buf.String(), "httpx: response marshal failed") {
		t.Fatalf("expected marshal failure to be logged, got: %q", buf.String())
	}
}

func TestNewTracingHTTPClient_UsesOtelTransport(t *testing.T) {
	client := NewTracingHTTPClient(5*time.Second, nil)
	if _, ok := client.Transport.(*otelhttp.Transport); !ok {
		t.Fatalf("expected otelhttp transport, got %T", client.Transport)
	}
}

// --- WriteError ---

func TestWriteError(t *testing.T) {
	tests := []struct {
		status   int
		wantCode string
	}{
		{http.StatusBadRequest, string(apperror.CodeValidation)},
		{http.StatusUnauthorized, string(apperror.CodeAuthRequired)},
		{http.StatusNotFound, string(apperror.CodeNotFound)},
		{http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED"},
		{http.StatusConflict, string(apperror.CodeConflict)},
		{http.StatusRequestTimeout, string(apperror.CodeTimeout)},
		{http.StatusRequestEntityTooLarge, string(apperror.CodePayloadTooLarge)},
		{http.StatusUnsupportedMediaType, "UNSUPPORTED_MEDIA_TYPE"},
		{http.StatusUnprocessableEntity, string(apperror.CodePermanent)},
		{http.StatusTooManyRequests, string(apperror.CodeRateLimit)},
		{http.StatusInternalServerError, "INTERNAL"},
	}

	for _, tt := range tests {
		t.Run(tt.wantCode, func(t *testing.T) {
			rec := httptest.NewRecorder()
			WriteError(rec, tt.status, "test error")

			if rec.Code != tt.status {
				t.Fatalf("expected %d, got %d", tt.status, rec.Code)
			}
			var got APIError
			if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if got.Code != tt.wantCode {
				t.Fatalf("expected code %q, got %q", tt.wantCode, got.Code)
			}
			if got.Error != "test error" {
				t.Fatalf("expected error %q, got %q", "test error", got.Error)
			}
		})
	}
}

// --- ParsePathID ---

func TestRequestPath_UsesEscapedPath(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/v1/files/a%2Fb", nil)
	if got := RequestPath(req); got != "/v1/files/a%2Fb" {
		t.Fatalf("RequestPath = %q, want escaped path", got)
	}
}

func TestRequestPath_NilSafe(t *testing.T) {
	if got := RequestPath(nil); got != "" {
		t.Fatalf("RequestPath(nil) = %q, want empty", got)
	}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.URL = nil
	if got := RequestPath(req); got != "" {
		t.Fatalf("RequestPath(nil URL) = %q, want empty", got)
	}
}

func TestParsePathID_ValidUUID(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/test/01961234-5678-7abc-8def-0123456789ab", nil)
	req.SetPathValue("id", "01961234-5678-7abc-8def-0123456789ab")
	rec := httptest.NewRecorder()

	id, ok := ParsePathID(rec, req, "id")
	if !ok {
		t.Fatal("expected ok=true")
	}
	if id != "01961234-5678-7abc-8def-0123456789ab" {
		t.Fatalf("unexpected id: %s", id)
	}
}

func TestParsePathID_InvalidUUID(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/test/not-a-uuid", nil)
	req.SetPathValue("id", "not-a-uuid")
	rec := httptest.NewRecorder()

	_, ok := ParsePathID(rec, req, "id")
	if ok {
		t.Fatal("expected ok=false for invalid UUID")
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

// ParsePathID must reject non-canonical UUID forms (urn:uuid: prefixes, braces,
// raw 32-hex, and uppercase). Accepting them lets many distinct path strings
// address one logical resource, splitting caches/audit logs/DB lookups.
func TestParsePathID_RejectsNonCanonicalForms(t *testing.T) {
	canonical := "01961234-5678-7abc-8def-0123456789ab"
	tests := []struct {
		name string
		raw  string
	}{
		{name: "uppercase", raw: "01961234-5678-7ABC-8DEF-0123456789AB"},
		{name: "urn prefix", raw: "urn:uuid:" + canonical},
		{name: "braced", raw: "{" + canonical + "}"},
		{name: "raw 32 hex", raw: "019612345678" + "7abc8def0123456789ab"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/test/id", nil)
			req.SetPathValue("id", tt.raw)
			rec := httptest.NewRecorder()

			_, ok := ParsePathID(rec, req, "id")
			if ok {
				t.Fatalf("expected ok=false for non-canonical form %q", tt.raw)
			}
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("expected 400 for %q, got %d", tt.raw, rec.Code)
			}
		})
	}
}

// The canonical form must still be accepted and returned verbatim.
func TestParsePathID_AcceptsCanonicalForm(t *testing.T) {
	canonical := "01961234-5678-7abc-8def-0123456789ab"
	req := httptest.NewRequest(http.MethodGet, "/test/id", nil)
	req.SetPathValue("id", canonical)
	rec := httptest.NewRecorder()

	id, ok := ParsePathID(rec, req, "id")
	if !ok {
		t.Fatal("expected ok=true for canonical UUID")
	}
	if id != canonical {
		t.Fatalf("id = %q, want %q", id, canonical)
	}
}

func TestParsePathID_NilRequest(t *testing.T) {
	rec := httptest.NewRecorder()

	_, ok := ParsePathID(rec, nil, "id")
	if ok {
		t.Fatal("expected ok=false for nil request")
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestParsePathID_NilRequestAndWriter(t *testing.T) {
	_, ok := ParsePathID(nil, nil, "id")
	if ok {
		t.Fatal("expected ok=false for nil request")
	}
}

// --- ParseID ---

func TestParseID_Valid(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/test/42", nil)
	req.SetPathValue("id", "42")

	id, ok := ParseID(req)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if id != 42 {
		t.Fatalf("expected 42, got %d", id)
	}
}

func TestParseID_Invalid(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/test/abc", nil)
	req.SetPathValue("id", "abc")

	_, ok := ParseID(req)
	if ok {
		t.Fatal("expected ok=false for non-numeric ID")
	}
}

func TestParseID_NilRequest(t *testing.T) {
	_, ok := ParseID(nil)
	if ok {
		t.Fatal("expected ok=false for nil request")
	}
}

// --- ParseBoolParam ---

func TestParseBoolParam(t *testing.T) {
	tests := []struct {
		value string
		want  *bool
	}{
		{"true", boolPtr(true)},
		{"1", boolPtr(true)},
		{"false", boolPtr(false)},
		{"0", boolPtr(false)},
		{"", nil},
		{"maybe", nil},
	}

	for _, tt := range tests {
		t.Run(tt.value, func(t *testing.T) {
			url := "/test"
			if tt.value != "" {
				url += "?enabled=" + tt.value
			}
			req := httptest.NewRequest(http.MethodGet, url, nil)
			got := ParseBoolParam(req, "enabled")

			if tt.want == nil {
				if got != nil {
					t.Fatalf("expected nil, got %v", *got)
				}
			} else {
				if got == nil {
					t.Fatalf("expected %v, got nil", *tt.want)
				}
				if *got != *tt.want {
					t.Fatalf("expected %v, got %v", *tt.want, *got)
				}
			}
		})
	}
}

func TestParseBoolParam_InvalidRequestOrAmbiguousKey(t *testing.T) {
	tests := []struct {
		name string
		req  *http.Request
		key  string
	}{
		{name: "nil request", req: nil, key: "enabled"},
		{name: "nil URL", req: &http.Request{}, key: "enabled"},
		{name: "empty key", req: httptest.NewRequest(http.MethodGet, "/test?=true", nil), key: ""},
		{name: "missing key", req: httptest.NewRequest(http.MethodGet, "/test", nil), key: "enabled"},
		{name: "duplicate key", req: httptest.NewRequest(http.MethodGet, "/test?enabled=true&enabled=false", nil), key: "enabled"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ParseBoolParam(tt.req, tt.key); got != nil {
				t.Fatalf("ParseBoolParam returned %v, want nil", *got)
			}
		})
	}
}

func boolPtr(b bool) *bool { return &b }

// --- DecodeJSON ---

func TestDecodeJSON_Success(t *testing.T) {
	body := `{"name":"test"}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	var dst struct {
		Name string `json:"name"`
	}
	ok := DecodeJSON(rec, req, &dst)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if dst.Name != "test" {
		t.Fatalf("expected name=test, got %s", dst.Name)
	}
}

func TestDecodeJSON_InvalidJSON(t *testing.T) {
	body := `{invalid`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	var dst struct{}
	ok := DecodeJSON(rec, req, &dst)
	if ok {
		t.Fatal("expected ok=false for invalid JSON")
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestDecodeJSON_TooLarge(t *testing.T) {
	// Create valid JSON body larger than the default 1MB cap.
	largeValue := strings.Repeat("A", maxBodySize+100)
	body := strings.NewReader(`{"data":"` + largeValue + `"}`)
	req := httptest.NewRequest(http.MethodPost, "/", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	var dst struct {
		Data string `json:"data"`
	}
	ok := DecodeJSON(rec, req, &dst)
	if ok {
		t.Fatal("expected ok=false for oversized body")
	}
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413, got %d", rec.Code)
	}
}

// --- WriteServiceError ---

// TestDecodeJSONWithLimit_RaisesCeiling verifies that DecodeJSONWithLimit
// can accept bodies larger than the default 1 MB DecodeJSON cap — the
// documented remedy for endpoints that legitimately exceed 1 MB JSON.
func TestDecodeJSONWithLimit_RaisesCeiling(t *testing.T) {
	// Body slightly over the default 1 MB maxBodySize but under a 2 MB limit.
	largeValue := strings.Repeat("A", maxBodySize+100)
	body := strings.NewReader(`{"data":"` + largeValue + `"}`)
	req := httptest.NewRequest(http.MethodPost, "/", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	var dst struct {
		Data string `json:"data"`
	}
	ok := DecodeJSONWithLimit(rec, req, &dst, int64(maxBodySize)*2)
	if !ok {
		t.Fatalf("expected ok=true for body under explicit limit, status=%d body=%s", rec.Code, rec.Body.String())
	}
	if dst.Data != largeValue {
		t.Fatal("decoded data mismatch")
	}
}

// TestDecodeJSONWithLimit_EnforcesExplicitCap still returns 413 when the
// body exceeds the caller-supplied limit.
func TestDecodeJSONWithLimit_EnforcesExplicitCap(t *testing.T) {
	largeValue := strings.Repeat("A", 200)
	body := strings.NewReader(`{"data":"` + largeValue + `"}`)
	req := httptest.NewRequest(http.MethodPost, "/", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	var dst struct {
		Data string `json:"data"`
	}
	ok := DecodeJSONWithLimit(rec, req, &dst, 64)
	if ok {
		t.Fatal("expected ok=false for body over explicit limit")
	}
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413, got %d", rec.Code)
	}
}

func TestWriteServiceError_NotFound(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/test", nil)

	WriteServiceError(rec, req, logger, apperror.NewNotFound("thing", "123"))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestWriteServiceError_Validation(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/test", nil)

	WriteServiceError(rec, req, logger, apperror.NewFieldValidation(apperror.FieldError{
		Field: "name", Message: "is required",
	}))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestWriteServiceError_Conflict(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/test", nil)

	WriteServiceError(rec, req, logger, apperror.NewConflict("already exists"))
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d", rec.Code)
	}
}

func TestWriteServiceError_Permanent(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/test", nil)

	WriteServiceError(rec, req, logger, apperror.NewPermanent("cannot proceed"))
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422, got %d", rec.Code)
	}
}

func TestWriteServiceError_UnhandledError(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/test", nil)

	WriteServiceError(rec, req, logger, errors.New("unexpected"))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rec.Code)
	}
}

func TestWriteServiceError_AuthRequired(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/test", nil)

	WriteServiceError(rec, req, logger, apperror.NewAuthRequired("session expired"))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestWriteServiceError_RateLimit(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/test", nil)

	WriteServiceError(rec, req, logger, apperror.NewRateLimitWithRetryAfter("quota exceeded", 30*time.Second))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d", rec.Code)
	}
	if rec.Header().Get("Retry-After") != "30" {
		t.Fatalf("expected Retry-After 30, got %q", rec.Header().Get("Retry-After"))
	}
}

func TestWriteServiceError_RateLimit_NoRetryAfter(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/test", nil)

	WriteServiceError(rec, req, logger, apperror.NewRateLimit("too fast"))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d", rec.Code)
	}
	if rec.Header().Get("Retry-After") != "" {
		t.Fatalf("expected no Retry-After header, got %q", rec.Header().Get("Retry-After"))
	}
}

func TestWriteServiceError_OperationFailed(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/test", nil)

	WriteServiceError(rec, req, logger, apperror.NewOperationFailed("pq: password auth failed for postgres://user:secret@10.0.0.5/app"))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rec.Code)
	}

	var body struct {
		Error string `json:"error"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("failed to decode body: %v", err)
	}
	if body.Error != "internal error" {
		t.Fatalf("expected 'internal error', got %q", body.Error)
	}
	if strings.Contains(rec.Body.String(), "secret") || strings.Contains(rec.Body.String(), "10.0.0.5") {
		t.Fatalf("operation failure response leaked internal details: %q", rec.Body.String())
	}
}

func TestWriteServiceError_Unavailable_NoDependency_503(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/test", nil)

	WriteServiceError(rec, req, logger, apperror.NewUnavailable("not ready"))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rec.Code)
	}
	if rec.Header().Get("Retry-After") != "" {
		t.Fatalf("expected no Retry-After header when RetryAfter is 0, got %q", rec.Header().Get("Retry-After"))
	}

	var body struct {
		Error string `json:"error"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("failed to decode body: %v", err)
	}
	if body.Error != "service unavailable" {
		t.Fatalf("expected 'service unavailable', got %q", body.Error)
	}
}

func TestWriteServiceError_DependencyUnavailable_502(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/test", nil)

	WriteServiceError(rec, req, logger, apperror.NewDependencyUnavailable("payment-service", "tcp dial timeout to 10.0.0.5:3000", errors.New("dial tcp 10.0.0.5:3000: i/o timeout")))
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d", rec.Code)
	}
	if rec.Header().Get("Retry-After") != "" {
		t.Fatalf("expected no Retry-After header for 502, got %q", rec.Header().Get("Retry-After"))
	}

	var body struct {
		Error string `json:"error"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("failed to decode body: %v", err)
	}
	// Must not leak internal details (IP addresses, ports, dependency names).
	if body.Error != "service unavailable" {
		t.Fatalf("expected 'service unavailable', got %q", body.Error)
	}
}

func TestWriteServiceError_Unavailable_WithRetryAfter(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/test", nil)

	ue := &apperror.UnavailableError{
		Message:    "not ready",
		RetryAfter: 30 * time.Second,
	}
	WriteServiceError(rec, req, logger, ue)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rec.Code)
	}
	if rec.Header().Get("Retry-After") != "30" {
		t.Fatalf("expected Retry-After 30, got %q", rec.Header().Get("Retry-After"))
	}
}

func TestWriteServiceError_DependencyUnavailable_WithRetryAfter(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/test", nil)

	ue := &apperror.UnavailableError{
		Message:    "redis down",
		Dependency: "redis",
		RetryAfter: 10 * time.Second,
	}
	WriteServiceError(rec, req, logger, ue)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d", rec.Code)
	}
	if rec.Header().Get("Retry-After") != "10" {
		t.Fatalf("expected Retry-After 10, got %q", rec.Header().Get("Retry-After"))
	}
}

func TestWriteServiceError_Unavailable_DoesNotLeakInternalDetails(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/test", nil)

	// The cause contains sensitive internal details.
	WriteServiceError(rec, req, logger, apperror.NewUnavailableWithCause(
		"connection to redis at 10.0.0.5:6379 refused",
		errors.New("dial tcp 10.0.0.5:6379: connection refused"),
	))

	var body struct {
		Error string `json:"error"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("failed to decode body: %v", err)
	}
	// The generic "service unavailable" message must be used, not the internal error.
	if body.Error != "service unavailable" {
		t.Fatalf("expected generic message, got %q", body.Error)
	}
	if strings.Contains(body.Error, "10.0.0.5") {
		t.Fatal("response body must not contain internal IP addresses")
	}
}

func TestWriteServiceError_Forbidden(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/test", nil)

	WriteServiceError(rec, req, logger, apperror.NewForbidden("access denied"))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rec.Code)
	}
}

// --- WriteValidationError ---

func TestWriteValidationError_WithFields(t *testing.T) {
	rec := httptest.NewRecorder()
	err := apperror.NewFieldValidation(
		apperror.FieldError{Field: "email", Message: "is required"},
		apperror.FieldError{Field: "name", Message: "too long"},
	)

	WriteValidationError(rec, nil, err)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}

	var resp struct {
		Error  string                `json:"error"`
		Code   string                `json:"code"`
		Fields []apperror.FieldError `json:"fields"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Code != "VALIDATION" {
		t.Fatalf("expected code VALIDATION, got %q", resp.Code)
	}
	if len(resp.Fields) != 2 {
		t.Fatalf("expected 2 fields, got %d", len(resp.Fields))
	}
}

func TestWriteValidationError_SimpleValidation(t *testing.T) {
	rec := httptest.NewRecorder()
	err := apperror.NewValidation("something went wrong")

	WriteValidationError(rec, nil, err)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

// --- NewServer ---

func TestNewServer_Defaults(t *testing.T) {
	srv := NewServer(":8080", http.NewServeMux())
	if srv.Addr != ":8080" {
		t.Fatalf("expected addr :8080, got %s", srv.Addr)
	}
	if srv.ReadTimeout != 15*time.Second {
		t.Fatalf("expected read timeout 15s, got %v", srv.ReadTimeout)
	}
	if srv.WriteTimeout != 35*time.Second {
		t.Fatalf("expected write timeout 35s, got %v", srv.WriteTimeout)
	}
}

func TestNewServer_WithWriteTimeout(t *testing.T) {
	srv := NewServer(":8080", http.NewServeMux(), WithWriteTimeout(0))
	if srv.WriteTimeout != 0 {
		t.Fatalf("expected write timeout 0, got %v", srv.WriteTimeout)
	}
}

// --- ParseID (additional edge cases) ---

func TestParseID_MissingPathValue(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	// No SetPathValue call — path value is empty string.
	_, ok := ParseID(req)
	if ok {
		t.Fatal("expected ok=false for missing path value")
	}
}

func TestParseID_Zero(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/test/0", nil)
	req.SetPathValue("id", "0")

	_, ok := ParseID(req)
	if ok {
		t.Fatal("expected ok=false for zero ID (auto-increment databases never use 0)")
	}
}

func TestParseID_NegativeValue(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/test/-1", nil)
	req.SetPathValue("id", "-1")

	_, ok := ParseID(req)
	if ok {
		t.Fatal("expected ok=false for negative ID")
	}
}

// --- ParsePathID (additional edge cases) ---

func TestParsePathID_MissingPathValue(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	rec := httptest.NewRecorder()

	_, ok := ParsePathID(rec, req, "id")
	if ok {
		t.Fatal("expected ok=false for missing path value")
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

// --- WriteError (default code mapping) ---

func TestWriteError_UnknownStatusMapsToInternal(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteError(rec, http.StatusTeapot, "I'm a teapot")

	if rec.Code != http.StatusTeapot {
		t.Fatalf("expected %d, got %d", http.StatusTeapot, rec.Code)
	}
	var got APIError
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Code != "INTERNAL" {
		t.Fatalf("expected code INTERNAL for unknown status, got %q", got.Code)
	}
}

// --- httpStatusToCode (direct) ---

func TestHttpStatusToCode(t *testing.T) {
	tests := []struct {
		status   int
		wantCode string
	}{
		{http.StatusBadRequest, string(apperror.CodeValidation)},
		{http.StatusUnauthorized, string(apperror.CodeAuthRequired)},
		{http.StatusNotFound, string(apperror.CodeNotFound)},
		{http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED"},
		{http.StatusConflict, string(apperror.CodeConflict)},
		{http.StatusRequestTimeout, string(apperror.CodeTimeout)},
		{http.StatusRequestEntityTooLarge, string(apperror.CodePayloadTooLarge)},
		{http.StatusUnsupportedMediaType, "UNSUPPORTED_MEDIA_TYPE"},
		{http.StatusUnprocessableEntity, string(apperror.CodePermanent)},
		{http.StatusTooManyRequests, string(apperror.CodeRateLimit)},
		{http.StatusInternalServerError, "INTERNAL"},
		{http.StatusForbidden, string(apperror.CodeForbidden)},
		{http.StatusBadGateway, "BAD_GATEWAY"},
		{http.StatusServiceUnavailable, string(apperror.CodeUnavailable)},
		{http.StatusInsufficientStorage, string(apperror.CodeStorageFull)},
		{http.StatusTeapot, "INTERNAL"},
		{999, "INTERNAL"},
	}

	for _, tt := range tests {
		got := httpStatusToCode(tt.status)
		if got != tt.wantCode {
			t.Errorf("httpStatusToCode(%d) = %q, want %q", tt.status, got, tt.wantCode)
		}
	}
}

// --- NewServer (additional option and default checks) ---

func TestNewServer_AllDefaults(t *testing.T) {
	srv := NewServer(":9090", http.NewServeMux())

	if srv.ReadHeaderTimeout != 5*time.Second {
		t.Fatalf("expected read header timeout 5s, got %v", srv.ReadHeaderTimeout)
	}
	if srv.IdleTimeout != 60*time.Second {
		t.Fatalf("expected idle timeout 60s, got %v", srv.IdleTimeout)
	}
	if srv.MaxHeaderBytes != 1<<20 {
		t.Fatalf("expected max header bytes 1MB, got %d", srv.MaxHeaderBytes)
	}
}

// The default ErrorLog routes through whatever slog.Default() resolves to at
// log time, so a slog.SetDefault performed after NewServer redirects
// connection-level error logs to the new handler instead of pinning the
// bootstrap one.
func TestNewServer_ErrorLogFollowsSetDefaultAfterConstruction(t *testing.T) {
	prev := slog.Default()
	defer slog.SetDefault(prev)

	// Construct with one default, then swap to a capturing default afterwards.
	slog.SetDefault(slog.New(slog.NewJSONHandler(io.Discard, nil)))
	srv := NewServer(":0", http.NewServeMux())

	var buf bytes.Buffer
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))

	srv.ErrorLog.Print("connection reset by peer 203.0.113.7")

	if buf.Len() == 0 {
		t.Fatal("ErrorLog did not follow slog.SetDefault performed after NewServer")
	}
	if !strings.Contains(buf.String(), "connection reset by peer") {
		t.Fatalf("ErrorLog output missing message: %s", buf.String())
	}
}

func TestNewServer_HTTP2HardeningRegistered(t *testing.T) {
	// `http2.ConfigureServer` registers the h2 ALPN handler in
	// TLSNextProto. Without the kit's call, srv.TLSNextProto is nil and
	// h2 would not be served over TLS. The kit only registers h2 when a
	// TLSConfig is configured — h2 requires TLS-ALPN negotiation and
	// plaintext h2 is h2c (caller's responsibility). The test therefore
	// supplies a TLSConfig to exercise the hardening path.
	srv := NewServer(":0", http.NewServeMux(), WithTLSConfig(&tls.Config{MinVersion: tls.VersionTLS12}))
	if _, ok := srv.TLSNextProto["h2"]; !ok {
		t.Fatalf("http2.ConfigureServer did not register h2 ALPN handler: %v", srv.TLSNextProto)
	}
}

func TestNewServer_HTTP2HardeningSkippedWithoutTLS(t *testing.T) {
	// Plaintext servers must NOT have TLSConfig auto-initialized — the
	// lifecycle wrapper uses srv.TLSConfig != nil as the TLS probe, so
	// auto-initialization would route ListenAndServe into
	// ListenAndServeTLS with empty cert/key and fail immediately.
	srv := NewServer(":0", http.NewServeMux())
	if srv.TLSConfig != nil {
		t.Fatalf("TLSConfig must be nil for plaintext NewServer, got %+v", srv.TLSConfig)
	}
}

func TestNewServer_HTTP2HandshakeSucceedsOverH2C(t *testing.T) {
	// This test verifies that an h2 client completes a handshake and
	// negotiates HTTP/2 against the kit-constructed server, confirming the
	// kit's server config is a valid BaseConfig for an http2.Server.
	//
	// NOTE: This does NOT exercise the kit's HTTP/2 frame-size /
	// MaxConcurrentStreams pins. Those are attached by http2.ConfigureServer
	// to srv.TLSNextProto, which ServeConn bypasses; only handler/timeouts
	// from BaseConfig are honored here. Crafting a raw oversized frame from
	// outside golang.org/x/net/http2 to assert the SETTINGS MaxFrameSize cap
	// is not feasible in-package, so the frame-size enforcement claimed by
	// docs/THREAT_MODEL (G-03) is not covered by this test.

	mux := http.NewServeMux()
	mux.HandleFunc("/ping", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	srv := NewServer(":0", mux)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	h2s := &http2.Server{}
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		h2s.ServeConn(conn, &http2.ServeConnOpts{
			Handler:    srv.Handler,
			BaseConfig: srv,
		})
	}()

	// Dial an h2c client and send one request to make sure the server is up.
	tr := &http2.Transport{
		AllowHTTP: true,
		DialTLSContext: func(_ context.Context, network, addr string, _ *tls.Config) (net.Conn, error) {
			return net.Dial(network, addr)
		},
	}
	client := &http.Client{Transport: tr, Timeout: 5 * time.Second}
	resp, err := client.Get("http://" + ln.Addr().String() + "/ping")
	if err != nil {
		t.Fatalf("h2c handshake: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	// The client successfully negotiated h2 against the kit-constructed
	// server over an h2c handshake. This only asserts protocol negotiation;
	// see the note above on why the frame-size pin is not covered here.
	if resp.ProtoMajor != 2 {
		t.Fatalf("ProtoMajor = %d, want 2 (HTTP/2)", resp.ProtoMajor)
	}
}

func TestNewServer_WithCustomWriteTimeout(t *testing.T) {
	srv := NewServer(":8080", http.NewServeMux(), WithWriteTimeout(30*time.Second))
	if srv.WriteTimeout != 30*time.Second {
		t.Fatalf("expected write timeout 30s, got %v", srv.WriteTimeout)
	}
	// Other defaults should remain.
	if srv.ReadTimeout != 15*time.Second {
		t.Fatalf("expected read timeout 15s unchanged, got %v", srv.ReadTimeout)
	}
}

// --- DecodeJSON (additional edge cases) ---

func TestDecodeJSON_EmptyBody(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(""))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	var dst struct {
		Name string `json:"name"`
	}
	ok := DecodeJSON(rec, req, &dst)
	if ok {
		t.Fatal("expected ok=false for empty body")
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestDecodeJSON_NilRequest(t *testing.T) {
	rec := httptest.NewRecorder()

	var dst struct{}
	ok := DecodeJSON(rec, nil, &dst)
	if ok {
		t.Fatal("expected ok=false for nil request")
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestDecodeJSON_NilBody(t *testing.T) {
	req := &http.Request{
		Header: http.Header{"Content-Type": {"application/json"}},
	}
	rec := httptest.NewRecorder()

	var dst struct{}
	ok := DecodeJSON(rec, req, &dst)
	if ok {
		t.Fatal("expected ok=false for nil body")
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestDecodeJSON_RejectsMissingContentType(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"name":"test"}`))
	rec := httptest.NewRecorder()

	var dst struct {
		Name string `json:"name"`
	}
	ok := DecodeJSON(rec, req, &dst)
	if ok {
		t.Fatal("expected ok=false for missing Content-Type")
	}
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("expected 415, got %d", rec.Code)
	}
}

func TestDecodeJSON_RejectsWrongContentType(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"name":"test"}`))
	req.Header.Set("Content-Type", "text/plain")
	rec := httptest.NewRecorder()

	var dst struct {
		Name string `json:"name"`
	}
	ok := DecodeJSON(rec, req, &dst)
	if ok {
		t.Fatal("expected ok=false for text/plain Content-Type")
	}
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("expected 415, got %d", rec.Code)
	}
}

func TestDecodeJSON_RejectsDuplicateContentType(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"name":"test"}`))
	req.Header.Add("Content-Type", "application/json")
	req.Header.Add("Content-Type", "text/plain")
	rec := httptest.NewRecorder()

	var dst struct {
		Name string `json:"name"`
	}
	ok := DecodeJSON(rec, req, &dst)
	if ok {
		t.Fatal("expected ok=false for duplicate Content-Type")
	}
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("expected 415, got %d", rec.Code)
	}
}

func TestDecodeJSON_RejectsInvalidContentTypeHeaderValue(t *testing.T) {
	for name, value := range map[string]string{
		"control":      "application/json\n",
		"invalid utf8": string([]byte{'a', 'p', 'p', 'l', 'i', 'c', 'a', 't', 'i', 'o', 'n', '/', 'j', 's', 'o', 'n', 0xff}),
	} {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"name":"test"}`))
			req.Header.Set("Content-Type", value)
			rec := httptest.NewRecorder()

			var dst struct {
				Name string `json:"name"`
			}
			ok := DecodeJSON(rec, req, &dst)
			if ok {
				t.Fatal("expected ok=false for invalid Content-Type")
			}
			if rec.Code != http.StatusUnsupportedMediaType {
				t.Fatalf("expected 415, got %d", rec.Code)
			}
		})
	}
}

func TestDecodeJSON_AcceptsStructuredJSONContentType(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"name":"test"}`))
	req.Header.Set("Content-Type", "application/merge-patch+json; charset=utf-8")
	rec := httptest.NewRecorder()

	var dst struct {
		Name string `json:"name"`
	}
	ok := DecodeJSON(rec, req, &dst)
	if !ok {
		t.Fatal("expected ok=true for +json Content-Type")
	}
	if dst.Name != "test" {
		t.Fatalf("expected name=test, got %s", dst.Name)
	}
}

// TestDecodeJSON_RejectsUnknownFields verifies the DisallowUnknownFields
// strictness guarantee: a body carrying a field absent from the destination
// type is a 400, not a silent drop. This is a security-relevant parser
// differential guard (clients cannot smuggle ignored fields).
func TestDecodeJSON_RejectsUnknownFields(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"name":"test","extra":"smuggled"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	var dst struct {
		Name string `json:"name"`
	}
	if DecodeJSON(rec, req, &dst) {
		t.Fatal("expected ok=false for unknown field")
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for unknown field, got %d", rec.Code)
	}
}

// TestDecodeJSON_RejectsTrailingData verifies the second-decode io.EOF guard
// that rejects bodies with a trailing top-level JSON value (e.g.
// `{"a":1} {"b":2}`), which dec.More() would not catch. Without this guard a
// caller could smuggle a second document past the parser.
func TestDecodeJSON_RejectsTrailingData(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"name":"first"} {"name":"second"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	var dst struct {
		Name string `json:"name"`
	}
	if DecodeJSON(rec, req, &dst) {
		t.Fatal("expected ok=false for trailing JSON value")
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for trailing data, got %d", rec.Code)
	}
}

// TestDecodeJSON_AllowsTrailingWhitespace confirms the trailing-data guard
// does not over-reject: whitespace after the first value is fine because the
// second decode still returns io.EOF.
func TestDecodeJSON_AllowsTrailingWhitespace(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("{\"name\":\"test\"}\n\t  "))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	var dst struct {
		Name string `json:"name"`
	}
	if !DecodeJSON(rec, req, &dst) {
		t.Fatalf("expected ok=true for trailing whitespace, got status %d", rec.Code)
	}
	if dst.Name != "test" {
		t.Fatalf("expected name=test, got %s", dst.Name)
	}
}

// --- NewServer panic on nil handler ---

func TestNewServer_PanicsOnEmptyAddr(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for empty address")
		}
	}()
	NewServer("", http.NewServeMux())
}

func TestNewServer_PanicsOnNilHandler(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for nil handler")
		}
	}()
	NewServer(":0", nil)
}

func TestNewServer_PanicsOnNilOption(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for nil option")
		}
	}()
	NewServer(":0", http.NewServeMux(), nil)
}

func TestWithWriteTimeout_PanicsOnNegative(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for negative write timeout")
		}
	}()
	WithWriteTimeout(-time.Second)
}

func TestWithTLSConfig_PanicsOnNil(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for nil TLS config")
		}
	}()
	WithTLSConfig(nil)
}

func TestWithTLSConfig_ClonesTLSConfigAndEnforcesFloor(t *testing.T) {
	cfg := &tls.Config{
		NextProtos: []string{"h2"},
	}
	cfg.MinVersion = minimumTLSVersion - 1

	srv := NewServer(":0", http.NewServeMux(), WithTLSConfig(cfg))
	if srv.TLSConfig == cfg {
		t.Fatal("server must own a cloned TLS config")
	}
	if cfg.MinVersion != minimumTLSVersion-1 {
		t.Fatalf("caller TLS config was mutated: got MinVersion %x", cfg.MinVersion)
	}
	if srv.TLSConfig.MinVersion != minimumTLSVersion {
		t.Fatalf("expected server TLS floor %x, got %x", minimumTLSVersion, srv.TLSConfig.MinVersion)
	}
	// http2.ConfigureServer (applied by NewServer to install the kit's
	// HTTP/2 hardening) appends "http/1.1" to NextProtos so ALPN
	// advertises both h1 and h2; the caller-supplied "h2" must still
	// be present, just no longer the only entry.
	if !containsString(srv.TLSConfig.NextProtos, "h2") {
		t.Fatalf("expected cloned TLS config to preserve NextProtos h2, got %v", srv.TLSConfig.NextProtos)
	}
}

func TestWithTLSConfig_ClonesAtOptionCreation(t *testing.T) {
	cfg := &tls.Config{
		NextProtos: []string{"h2"},
		ServerName: "before.example",
	}
	opt := WithTLSConfig(cfg)

	cfg.NextProtos[0] = "http/1.1"
	cfg.ServerName = "after.example"

	srv := NewServer(":0", http.NewServeMux(), opt)
	// NewServer enables HTTP/2 hardening via http2.ConfigureServer
	// which appends "http/1.1" to NextProtos; the snapshot guarantee
	// is that the post-option mutation of the caller's slice did NOT
	// leak through.
	if !containsString(srv.TLSConfig.NextProtos, "h2") {
		t.Fatalf("expected option to snapshot NextProtos h2, got %v", srv.TLSConfig.NextProtos)
	}
	if srv.TLSConfig.ServerName != "before.example" {
		t.Fatalf("expected option to snapshot ServerName, got %q", srv.TLSConfig.ServerName)
	}
}

func containsString(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

func TestWithTLSConfig_PanicsWhenMaxVersionBelowFloor(t *testing.T) {
	cfg := &tls.Config{}
	cfg.MaxVersion = minimumTLSVersion - 1

	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for impossible TLS version range")
		}
	}()
	NewServer(":0", http.NewServeMux(), WithTLSConfig(cfg))
}

func TestWithErrorLog_PanicsOnNil(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for nil error logger")
		}
	}()
	WithErrorLog(nil)
}

// --- NewHTTPClient panic on non-positive timeout ---

func TestNewHTTPClient_PanicsOnZeroTimeout(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for zero timeout")
		}
	}()
	NewHTTPClient(0, nil)
}

func TestNewHTTPClient_PanicsOnNegativeTimeout(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for negative timeout")
		}
	}()
	NewHTTPClient(-1*time.Second, nil)
}

func TestNewHTTPClient_PanicsOnNilOption(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for nil option")
		}
	}()
	NewHTTPClient(time.Second, nil, nil)
}

func TestNewHTTPClient_BlocksRedirectsByDefault(t *testing.T) {
	srv := newClientRedirectTestServer(t)

	resp, err := NewHTTPClient(time.Second, nil).Get(srv.URL + "/start")
	if resp != nil {
		_ = resp.Body.Close()
	}
	if !errors.Is(err, ErrRedirectBlocked) {
		t.Fatalf("Get redirect error = %v, want ErrRedirectBlocked", err)
	}
}

func TestNewHTTPClient_WithFollowRedirects(t *testing.T) {
	srv := newClientRedirectTestServer(t)
	client := NewHTTPClient(time.Second, nil, WithFollowRedirects(1))

	resp, err := client.Get(srv.URL + "/start")
	if err != nil {
		t.Fatalf("Get redirect: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if got := string(body); got != "ok" {
		t.Fatalf("body = %q, want ok", got)
	}
}

func TestNewHTTPClient_WithFollowRedirectsStopsAtHopLimit(t *testing.T) {
	srv := newClientRedirectTestServer(t)
	client := NewHTTPClient(time.Second, nil, WithFollowRedirects(1))

	resp, err := client.Get(srv.URL + "/chain-a")
	if resp != nil {
		_ = resp.Body.Close()
	}
	if !errors.Is(err, ErrRedirectLimitExceeded) {
		t.Fatalf("Get redirect error = %v, want ErrRedirectLimitExceeded", err)
	}
}

func TestWithFollowRedirects_PanicsOnNonPositive(t *testing.T) {
	tests := []int{0, -1}
	for _, maxHops := range tests {
		t.Run(strconv.Itoa(maxHops), func(t *testing.T) {
			defer func() {
				if r := recover(); r == nil {
					t.Fatal("expected panic")
				}
			}()
			WithFollowRedirects(maxHops)
		})
	}
}

func TestNewHTTPClient_ClonesTLSConfigAndEnforcesFloor(t *testing.T) {
	cfg := &tls.Config{
		ServerName: "api.internal.test",
	}
	cfg.MinVersion = minimumTLSVersion - 1

	client := NewHTTPClient(time.Second, cfg)
	tr, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("expected *http.Transport, got %T", client.Transport)
	}
	if tr.TLSClientConfig == cfg {
		t.Fatal("client transport must own a cloned TLS config")
	}
	if cfg.MinVersion != minimumTLSVersion-1 {
		t.Fatalf("caller TLS config was mutated: got MinVersion %x", cfg.MinVersion)
	}
	if tr.TLSClientConfig.MinVersion != minimumTLSVersion {
		t.Fatalf("expected client TLS floor %x, got %x", minimumTLSVersion, tr.TLSClientConfig.MinVersion)
	}
	if tr.TLSClientConfig.ServerName != "api.internal.test" {
		t.Fatalf("expected cloned TLS config to preserve ServerName, got %q", tr.TLSClientConfig.ServerName)
	}
}

func TestNewHTTPClient_PanicsWhenTLSMaxVersionBelowFloor(t *testing.T) {
	cfg := &tls.Config{}
	cfg.MaxVersion = minimumTLSVersion - 1

	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for impossible TLS version range")
		}
	}()
	NewHTTPClient(time.Second, cfg)
}

func TestWithIdleConnTimeout_PanicsOnNegative(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for negative idle timeout")
		}
	}()
	WithIdleConnTimeout(-time.Second)
}

func TestNewTracingHTTPClient_PanicsOnZeroTimeout(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for zero timeout")
		}
	}()
	NewTracingHTTPClient(0, nil)
}

func TestNewTracingHTTPClient_BlocksRedirectsByDefault(t *testing.T) {
	srv := newClientRedirectTestServer(t)

	resp, err := NewTracingHTTPClient(time.Second, nil).Get(srv.URL + "/start")
	if resp != nil {
		_ = resp.Body.Close()
	}
	if !errors.Is(err, ErrRedirectBlocked) {
		t.Fatalf("Get redirect error = %v, want ErrRedirectBlocked", err)
	}
}

func TestNewTracingHTTPClient_WithKitFollowRedirects(t *testing.T) {
	srv := newClientRedirectTestServer(t)
	client := NewTracingHTTPClient(time.Second, nil, WithKitOption(WithFollowRedirects(1)))

	resp, err := client.Get(srv.URL + "/start")
	if err != nil {
		t.Fatalf("Get redirect: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
}

func TestNewTracingHTTPClient_PanicsOnNilOption(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for nil tracing option")
		}
	}()
	NewTracingHTTPClient(time.Second, nil, nil)
}

func TestWithKitOption_PanicsOnNil(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for nil kit option")
		}
	}()
	WithKitOption(nil)
}

// --- newKitTransport handles replaced http.DefaultTransport ---

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestNewHTTPClient_HandlesReplacedDefaultTransport(t *testing.T) {
	prev := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = prev })
	http.DefaultTransport = roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("not used")
	})

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("NewHTTPClient must not panic when DefaultTransport is not *http.Transport: %v", r)
		}
	}()
	c := NewHTTPClient(5*time.Second, nil)
	if c == nil || c.Transport == nil {
		t.Fatal("expected client with transport")
	}
	if _, ok := c.Transport.(*http.Transport); !ok {
		t.Fatalf("expected *http.Transport fallback, got %T", c.Transport)
	}
}

func TestNewTracingHTTPClient_HandlesReplacedDefaultTransport(t *testing.T) {
	prev := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = prev })
	http.DefaultTransport = roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("not used")
	})

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("NewTracingHTTPClient must not panic when DefaultTransport is not *http.Transport: %v", r)
		}
	}()
	c := NewTracingHTTPClient(5*time.Second, nil)
	if c == nil || c.Transport == nil {
		t.Fatal("expected client with transport")
	}
}

func TestNewResilientHTTPClient_HandlesReplacedDefaultTransport(t *testing.T) {
	prev := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = prev })
	http.DefaultTransport = roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("not used")
	})

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("NewResilientHTTPClient must not panic when DefaultTransport is not *http.Transport: %v", r)
		}
	}()
	c := NewResilientHTTPClient()
	if c == nil || c.Transport == nil {
		t.Fatal("expected client with transport")
	}
}

func TestNewResilientHTTPClient_ClonesTLSConfigAndEnforcesFloor(t *testing.T) {
	cfg := &tls.Config{ServerName: "api.internal.test"}
	cfg.MinVersion = minimumTLSVersion - 1

	client := NewResilientHTTPClient(WithResilientTLS(cfg))
	cb, ok := client.Transport.(*circuitBreakerTransport)
	if !ok {
		t.Fatalf("expected *circuitBreakerTransport, got %T", client.Transport)
	}
	tr, ok := cb.base.(*http.Transport)
	if !ok {
		t.Fatalf("expected inner *http.Transport, got %T", cb.base)
	}
	if tr.TLSClientConfig == cfg {
		t.Fatal("resilient client transport must own a cloned TLS config")
	}
	if cfg.MinVersion != minimumTLSVersion-1 {
		t.Fatalf("caller TLS config was mutated: got MinVersion %x", cfg.MinVersion)
	}
	if tr.TLSClientConfig.MinVersion != minimumTLSVersion {
		t.Fatalf("expected resilient client TLS floor %x, got %x", minimumTLSVersion, tr.TLSClientConfig.MinVersion)
	}
	if tr.TLSClientConfig.ServerName != "api.internal.test" {
		t.Fatalf("expected cloned TLS config to preserve ServerName, got %q", tr.TLSClientConfig.ServerName)
	}
	if tr.MaxIdleConnsPerHost != defaultMaxIdleConnsPerHost {
		t.Fatalf("expected MaxIdleConnsPerHost %d, got %d", defaultMaxIdleConnsPerHost, tr.MaxIdleConnsPerHost)
	}
}

func TestWithResilientTLS_ClonesAtOptionCreation(t *testing.T) {
	cfg := &tls.Config{
		NextProtos: []string{"h2"},
		ServerName: "before.example",
	}
	opt := WithResilientTLS(cfg)

	cfg.NextProtos[0] = "http/1.1"
	cfg.ServerName = "after.example"

	client := NewResilientHTTPClient(opt)
	cb, ok := client.Transport.(*circuitBreakerTransport)
	if !ok {
		t.Fatalf("expected *circuitBreakerTransport, got %T", client.Transport)
	}
	tr, ok := cb.base.(*http.Transport)
	if !ok {
		t.Fatalf("expected inner *http.Transport, got %T", cb.base)
	}
	if got := strings.Join(tr.TLSClientConfig.NextProtos, ","); got != "h2" {
		t.Fatalf("expected option to snapshot NextProtos, got %q", got)
	}
	if tr.TLSClientConfig.ServerName != "before.example" {
		t.Fatalf("expected option to snapshot ServerName, got %q", tr.TLSClientConfig.ServerName)
	}
}

func TestNewResilientHTTPClient_WithIdleConnTimeout(t *testing.T) {
	client := NewResilientHTTPClient(WithResilientIdleConnTimeout(15 * time.Second))
	cb, ok := client.Transport.(*circuitBreakerTransport)
	if !ok {
		t.Fatalf("expected *circuitBreakerTransport, got %T", client.Transport)
	}
	tr, ok := cb.base.(*http.Transport)
	if !ok {
		t.Fatalf("expected inner *http.Transport, got %T", cb.base)
	}
	if tr.IdleConnTimeout != 15*time.Second {
		t.Fatalf("expected IdleConnTimeout 15s, got %v", tr.IdleConnTimeout)
	}
}

func TestNewResilientHTTPClient_PanicsWhenTLSMaxVersionBelowFloor(t *testing.T) {
	cfg := &tls.Config{}
	cfg.MaxVersion = minimumTLSVersion - 1

	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for impossible TLS version range")
		}
	}()
	NewResilientHTTPClient(WithResilientTLS(cfg))
}

func TestNewResilientHTTPClient_BlocksRedirectsByDefault(t *testing.T) {
	srv := newClientRedirectTestServer(t)

	resp, err := NewResilientHTTPClient().Get(srv.URL + "/start")
	if resp != nil {
		_ = resp.Body.Close()
	}
	if !errors.Is(err, ErrRedirectBlocked) {
		t.Fatalf("Get redirect error = %v, want ErrRedirectBlocked", err)
	}
}

func TestNewResilientHTTPClient_WithFollowRedirects(t *testing.T) {
	srv := newClientRedirectTestServer(t)
	client := NewResilientHTTPClient(WithResilientFollowRedirects(1))

	resp, err := client.Get(srv.URL + "/start")
	if err != nil {
		t.Fatalf("Get redirect: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
}

func TestWithCBShouldTrip_PanicsOnNil(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on nil predicate")
		}
	}()
	WithCBShouldTrip(nil)
}

func TestWithCBOnStateChange_PanicsOnNil(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on nil callback")
		}
	}()
	WithCBOnStateChange(nil)
}

func TestResilientOptions_PanicOnInvalidInput(t *testing.T) {
	tests := []struct {
		name string
		fn   func()
	}{
		{name: "timeout zero", fn: func() { WithResilientTimeout(0) }},
		{name: "timeout negative", fn: func() { WithResilientTimeout(-time.Second) }},
		{name: "tls nil", fn: func() { WithResilientTLS(nil) }},
		{name: "idle timeout negative", fn: func() { WithResilientIdleConnTimeout(-time.Second) }},
		{name: "threshold zero", fn: func() { WithCBThreshold(0) }},
		{name: "threshold negative", fn: func() { WithCBThreshold(-1) }},
		{name: "reset zero", fn: func() { WithCBResetTimeout(0) }},
		{name: "reset negative", fn: func() { WithCBResetTimeout(-time.Second) }},
		{name: "redirects zero", fn: func() { WithResilientFollowRedirects(0) }},
		{name: "redirects negative", fn: func() { WithResilientFollowRedirects(-1) }},
		{name: "deadline option nil", fn: func() { NewResilientHTTPClient(WithDeadlineBudget(nil)) }},
		{name: "resilient option nil", fn: func() { NewResilientHTTPClient(nil) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer func() {
				if r := recover(); r == nil {
					t.Fatal("expected panic")
				}
			}()
			tt.fn()
		})
	}
}

func TestWithoutResilientTimeout(t *testing.T) {
	client := NewResilientHTTPClient(WithoutResilientTimeout())
	if client.Timeout != 0 {
		t.Fatalf("expected timeout disabled, got %v", client.Timeout)
	}
}

func newClientRedirectTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/start", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/next", http.StatusFound)
	})
	mux.HandleFunc("/next", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("/chain-a", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/chain-b", http.StatusFound)
	})
	mux.HandleFunc("/chain-b", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/chain-c", http.StatusFound)
	})
	mux.HandleFunc("/chain-c", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("done"))
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}
