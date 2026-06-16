package middleware

import (
	"bytes"
	"io"
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
	if rec.WroteHeader() {
		t.Error("new recorder should not report a written header")
	}
	_, _ = rec.Write([]byte("hello"))

	if !rec.WroteHeader() {
		t.Error("Write should set wroteHeader flag")
	}
}

// interimRecorder records every WriteHeader call so the test can assert that
// 1xx interim responses are forwarded to the underlying writer.
type interimRecorder struct {
	http.ResponseWriter
	codes []int
}

func (i *interimRecorder) WriteHeader(code int) {
	i.codes = append(i.codes, code)
	i.ResponseWriter.WriteHeader(code)
}

func TestResponseRecorder_InterimResponseThenFinalStatus(t *testing.T) {
	inner := &interimRecorder{ResponseWriter: httptest.NewRecorder()}
	rec := NewResponseRecorder(inner)

	// net/http allows a 1xx interim response (e.g. Early Hints) followed by
	// the final status code. The recorder must not latch on the 1xx code.
	rec.WriteHeader(http.StatusEarlyHints) // 103
	rec.WriteHeader(http.StatusCreated)    // 201 (final)

	if rec.Status() != http.StatusCreated {
		t.Errorf("status = %d, want 201 (final status after 1xx interim)", rec.Status())
	}
	if rec.WroteHeader() != true {
		t.Errorf("WroteHeader() = false, want true after final status")
	}
	wantCodes := []int{http.StatusEarlyHints, http.StatusCreated}
	if len(inner.codes) != len(wantCodes) {
		t.Fatalf("underlying WriteHeader codes = %v, want %v", inner.codes, wantCodes)
	}
	for idx, c := range wantCodes {
		if inner.codes[idx] != c {
			t.Fatalf("underlying WriteHeader codes = %v, want %v", inner.codes, wantCodes)
		}
	}
}

func TestResponseRecorder_InterimResponseThenWrite(t *testing.T) {
	inner := &interimRecorder{ResponseWriter: httptest.NewRecorder()}
	rec := NewResponseRecorder(inner)

	rec.WriteHeader(http.StatusEarlyHints) // 103 interim
	if rec.WroteHeader() {
		t.Error("a 1xx interim response should not mark the header as written")
	}
	_, _ = rec.Write([]byte("body")) // implicit final 200

	if rec.Status() != http.StatusOK {
		t.Errorf("status = %d, want 200 (implicit final after 1xx interim)", rec.Status())
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

type pushRecorder struct {
	http.ResponseWriter
	target string
}

func (p *pushRecorder) Push(target string, _ *http.PushOptions) error {
	p.target = target
	return nil
}

func TestResponseRecorder_PushForwarded(t *testing.T) {
	inner := &pushRecorder{ResponseWriter: httptest.NewRecorder()}
	rec := NewResponseRecorder(inner)

	if err := rec.Push("/asset.js", nil); err != nil {
		t.Fatalf("Push returned error: %v", err)
	}
	if inner.target != "/asset.js" {
		t.Fatalf("target = %q, want /asset.js", inner.target)
	}
}

func TestResponseRecorder_PushUnsupported(t *testing.T) {
	rec := NewResponseRecorder(httptest.NewRecorder())
	if err := rec.Push("/asset.js", nil); err != http.ErrNotSupported {
		t.Fatalf("Push error = %v, want http.ErrNotSupported", err)
	}
}

type readerFromRecorder struct {
	http.ResponseWriter
	body bytes.Buffer
}

func (r *readerFromRecorder) ReadFrom(src io.Reader) (int64, error) {
	return r.body.ReadFrom(src)
}

func TestResponseRecorder_ReadFromForwarded(t *testing.T) {
	inner := &readerFromRecorder{ResponseWriter: httptest.NewRecorder()}
	rec := NewResponseRecorder(inner)

	n, err := rec.ReadFrom(bytes.NewBufferString("hello"))
	if err != nil {
		t.Fatalf("ReadFrom returned error: %v", err)
	}
	if n != 5 || inner.body.String() != "hello" {
		t.Fatalf("ReadFrom copied %d %q, want 5 hello", n, inner.body.String())
	}
	if rec.Status() != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Status())
	}
}

func TestResponseRecorder_ReadFromFallback(t *testing.T) {
	inner := httptest.NewRecorder()
	rec := NewResponseRecorder(inner)

	n, err := rec.ReadFrom(bytes.NewBufferString("hello"))
	if err != nil {
		t.Fatalf("ReadFrom returned error: %v", err)
	}
	if n != 5 || inner.Body.String() != "hello" {
		t.Fatalf("ReadFrom copied %d %q, want 5 hello", n, inner.Body.String())
	}
	if rec.Status() != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Status())
	}
}
