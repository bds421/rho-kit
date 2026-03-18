package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestResponseRecorder_DefaultStatus(t *testing.T) {
	rec := NewResponseRecorder(httptest.NewRecorder())
	if rec.Status() != http.StatusOK {
		t.Errorf("default status = %d, want 200", rec.Status())
	}
}

func TestResponseRecorder_CapturesWriteHeader(t *testing.T) {
	rec := NewResponseRecorder(httptest.NewRecorder())
	rec.WriteHeader(http.StatusNotFound)

	if rec.Status() != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Status())
	}
}

func TestResponseRecorder_DoubleWriteHeader(t *testing.T) {
	rec := NewResponseRecorder(httptest.NewRecorder())
	rec.WriteHeader(http.StatusCreated)
	rec.WriteHeader(http.StatusNotFound) // second call should not change captured code

	if rec.Status() != http.StatusCreated {
		t.Errorf("status = %d, want 201 (first WriteHeader)", rec.Status())
	}
}

func TestResponseRecorder_WriteImplicitHeader(t *testing.T) {
	rec := NewResponseRecorder(httptest.NewRecorder())
	_, _ = rec.Write([]byte("hello"))

	if !rec.wroteHeader {
		t.Error("Write should set wroteHeader flag")
	}
}

func TestResponseRecorder_Flush(t *testing.T) {
	inner := httptest.NewRecorder()
	rec := NewResponseRecorder(inner)
	// httptest.ResponseRecorder implements http.Flusher
	rec.Flush() // should not panic
	if !inner.Flushed {
		t.Error("inner recorder should be flushed")
	}
}

func TestResponseRecorder_Hijack_NotSupported(t *testing.T) {
	rec := NewResponseRecorder(httptest.NewRecorder())
	_, _, err := rec.Hijack()
	if err == nil {
		t.Error("expected error for non-hijackable writer")
	}
}

func TestResponseRecorder_Unwrap(t *testing.T) {
	inner := httptest.NewRecorder()
	rec := NewResponseRecorder(inner)
	if rec.Unwrap() != inner {
		t.Error("Unwrap should return the underlying ResponseWriter")
	}
}
