package maxbody

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMaxBodySize_UnderLimit(t *testing.T) {
	handler := MaxBodySize(1024)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("unexpected read error: %v", err)
		}
		if string(body) != "hello" {
			t.Errorf("body = %q, want %q", string(body), "hello")
		}
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("hello"))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

func TestMaxBodySize_OverLimit(t *testing.T) {
	var readErr error
	handler := MaxBodySize(5)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, readErr = io.ReadAll(r.Body)
		// MaxBodySize installs http.MaxBytesReader, so the read must fail with
		// *http.MaxBytesError specifically (not just any error).
		var maxErr *http.MaxBytesError
		if !errors.As(readErr, &maxErr) {
			t.Fatalf("read error = %v, want *http.MaxBytesError", readErr)
		}
		if maxErr.Limit != 5 {
			t.Errorf("MaxBytesError.Limit = %d, want 5", maxErr.Limit)
		}
	}))

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("this is longer than 5 bytes"))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	// The middleware itself does NOT write a status (per its documented
	// contract); the handler here read the body but wrote nothing, so the
	// recorder retains its default 200. This pins that 413 translation is the
	// decode helper's job, not the middleware's.
	if rec.Code != http.StatusOK {
		t.Fatalf("middleware must not write a status itself; got %d", rec.Code)
	}
	if readErr == nil {
		t.Fatal("expected the handler to observe a read error")
	}
}

func TestMaxBodySize_NilBody(t *testing.T) {
	handler := MaxBodySize(1024)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Body = nil
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for nil body, got %d", rec.Code)
	}
}
