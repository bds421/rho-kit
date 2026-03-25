package httpx

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"github.com/bds421/rho-kit/core/apperror"
)

// --- WriteJSON ---

func TestWriteJSON_Success(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteJSON(rec, http.StatusOK, map[string]string{"key": "value"})

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
	// Channels cannot be marshaled to JSON
	WriteJSON(rec, http.StatusOK, make(chan int))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "internal error") {
		t.Fatalf("expected internal error in body, got: %s", rec.Body.String())
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
		{http.StatusBadRequest, "VALIDATION"},
		{http.StatusUnauthorized, "UNAUTHORIZED"},
		{http.StatusNotFound, "NOT_FOUND"},
		{http.StatusConflict, "CONFLICT"},
		{http.StatusRequestEntityTooLarge, "PAYLOAD_TOO_LARGE"},
		{http.StatusUnprocessableEntity, "UNPROCESSABLE_ENTITY"},
		{http.StatusTooManyRequests, "RATE_LIMITED"},
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
	// Create valid JSON body larger than MaxBodySize (1MB):
	// {"data":"AAAA...AAA"} where the value is large enough to exceed the limit.
	largeValue := strings.Repeat("A", MaxBodySize+100)
	body := strings.NewReader(`{"data":"` + largeValue + `"}`)
	req := httptest.NewRequest(http.MethodPost, "/", body)
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

	WriteServiceError(rec, req, logger, apperror.NewRateLimit("quota exceeded", 30*time.Second))
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

	WriteServiceError(rec, req, logger, apperror.NewRateLimit("too fast", 0))
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

	WriteServiceError(rec, req, logger, apperror.NewOperationFailed("payment declined"))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rec.Code)
	}

	var body struct {
		Error string `json:"error"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("failed to decode body: %v", err)
	}
	if body.Error != "payment declined" {
		t.Fatalf("expected 'payment declined', got %q", body.Error)
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

// --- RequestID context ---

func TestRequestID_SetAndGet(t *testing.T) {
	ctx := SetRequestID(context.Background(), "test-id-123")
	got := RequestID(ctx)
	if got != "test-id-123" {
		t.Fatalf("expected test-id-123, got %q", got)
	}
}

func TestRequestID_Missing(t *testing.T) {
	got := RequestID(context.Background())
	if got != "" {
		t.Fatalf("expected empty string, got %q", got)
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
		{http.StatusBadRequest, "VALIDATION"},
		{http.StatusUnauthorized, "UNAUTHORIZED"},
		{http.StatusNotFound, "NOT_FOUND"},
		{http.StatusConflict, "CONFLICT"},
		{http.StatusRequestEntityTooLarge, "PAYLOAD_TOO_LARGE"},
		{http.StatusUnprocessableEntity, "UNPROCESSABLE_ENTITY"},
		{http.StatusTooManyRequests, "RATE_LIMITED"},
		{http.StatusInternalServerError, "INTERNAL"},
		{http.StatusForbidden, "FORBIDDEN"},
		{http.StatusBadGateway, "BAD_GATEWAY"},
		{http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE"},
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
