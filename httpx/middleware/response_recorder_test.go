package middleware_test

import (
	"bytes"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/bds421/rho-kit/httpx/v2/middleware"
	"github.com/bds421/rho-kit/httpx/v2/middleware/metrics"
	"github.com/bds421/rho-kit/httpx/v2/middleware/stack"
	"github.com/bds421/rho-kit/httpx/v2/middleware/timeout"
	"github.com/prometheus/client_golang/prometheus"
)

func TestResponseRecorder_DefaultStatus(t *testing.T) {
	rec := middleware.NewResponseRecorder(httptest.NewRecorder())
	if rec.Status() != http.StatusOK {
		t.Errorf("default status = %d, want 200", rec.Status())
	}
}

func TestResponseRecorder_CapturesWriteHeader(t *testing.T) {
	rec := middleware.NewResponseRecorder(httptest.NewRecorder())
	rec.WriteHeader(http.StatusNotFound)

	if rec.Status() != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Status())
	}
}

func TestResponseRecorder_DoubleWriteHeader(t *testing.T) {
	rec := middleware.NewResponseRecorder(httptest.NewRecorder())
	rec.WriteHeader(http.StatusCreated)
	rec.WriteHeader(http.StatusNotFound) // second call should not change captured code

	if rec.Status() != http.StatusCreated {
		t.Errorf("status = %d, want 201 (first WriteHeader)", rec.Status())
	}
}

func TestResponseRecorder_WriteImplicitHeader(t *testing.T) {
	rec := middleware.NewResponseRecorder(httptest.NewRecorder())
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
	rec := middleware.NewResponseRecorder(inner)

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
	rec := middleware.NewResponseRecorder(inner)

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
	rec := middleware.NewResponseRecorder(inner)
	// httptest.ResponseRecorder implements http.Flusher
	rec.Flush() // should not panic
	if !inner.Flushed {
		t.Error("inner recorder should be flushed")
	}
}

// TestResponseRecorder_FlushLatchesHeader verifies that Flush mirrors
// net/http's implicit header commit: http.Flusher.Flush sends a 200 header
// when none was written, so the recorder must mark wroteHeader=true (status
// stays 200). Otherwise panic-recovery consumers see WroteHeader()==false and
// report 500 while the wire already carried 200, desyncing the recorder from
// the committed response.
func TestResponseRecorder_FlushLatchesHeader(t *testing.T) {
	inner := httptest.NewRecorder()
	rec := middleware.NewResponseRecorder(inner)

	if rec.WroteHeader() {
		t.Fatal("new recorder should not report a written header")
	}
	rec.Flush()

	if !rec.WroteHeader() {
		t.Error("Flush should latch wroteHeader=true (implicit 200 commit)")
	}
	if rec.Status() != http.StatusOK {
		t.Errorf("status = %d, want 200 after implicit Flush commit", rec.Status())
	}
}

// TestResponseRecorder_FlushPreservesStatus verifies Flush does not overwrite a
// status code already set via WriteHeader.
func TestResponseRecorder_FlushPreservesStatus(t *testing.T) {
	inner := httptest.NewRecorder()
	rec := middleware.NewResponseRecorder(inner)

	rec.WriteHeader(http.StatusAccepted) // 202
	rec.Flush()

	if rec.Status() != http.StatusAccepted {
		t.Errorf("status = %d, want 202 (Flush must not overwrite an explicit status)", rec.Status())
	}
}

// nonFlusher wraps a ResponseWriter but does not implement http.Flusher, so
// Flush is a no-op on the wire and must not latch the recorder's header state.
type nonFlusher struct {
	header http.Header
}

func (n *nonFlusher) Header() http.Header {
	if n.header == nil {
		n.header = make(http.Header)
	}
	return n.header
}

func (n *nonFlusher) Write(b []byte) (int, error) { return len(b), nil }
func (n *nonFlusher) WriteHeader(int)             {}

func TestResponseRecorder_FlushNonFlusherDoesNotLatch(t *testing.T) {
	rec := middleware.NewResponseRecorder(&nonFlusher{})

	rec.Flush() // underlying writer is not a Flusher: nothing committed

	if rec.WroteHeader() {
		t.Error("Flush on a non-Flusher writer must not latch wroteHeader")
	}
}

// The declared native fixture separates the error-reporting operation from the
// old void interface. Embedding ResponseWriter hides the recorder's own Flush.
type errorFlushWriter struct {
	http.ResponseWriter
	inner  *httptest.ResponseRecorder
	err    error
	calls  int
	before func()
}

func (w *errorFlushWriter) FlushError() error {
	w.calls++
	if w.before != nil {
		w.before()
	}
	w.inner.Flush()
	return w.err
}

type bothFlushWriter struct {
	*errorFlushWriter
	voidCalls int
}

func (w *bothFlushWriter) Flush() { w.voidCalls++; w.inner.Flush() }

type unwrapOnlyWriter struct{ http.ResponseWriter }

func (w unwrapOnlyWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func TestResponseRecorder_ControllerFlushPreservesNativeErrorAndStatus(t *testing.T) {
	for _, mode := range []string{"success", "native-error", "both-interfaces", "nested", "unwrap-only", "explicit-status", "native-unsupported"} {
		t.Run(mode, func(t *testing.T) {
			inner := httptest.NewRecorder()
			native := &errorFlushWriter{ResponseWriter: inner, inner: inner}
			if mode != "success" {
				native.err = errors.New("invented native flush failure")
			}
			if mode == "native-unsupported" {
				native.err = http.ErrNotSupported
			}
			var writer http.ResponseWriter = native
			both := &bothFlushWriter{errorFlushWriter: native}
			if mode == "both-interfaces" {
				writer = both
			}
			if mode == "unwrap-only" {
				writer = unwrapOnlyWriter{ResponseWriter: writer}
			}
			var nested []*middleware.ResponseRecorder
			if mode == "nested" {
				for range 3 {
					rec := middleware.NewResponseRecorder(writer)
					nested = append(nested, rec)
					writer = rec
				}
			}
			rec := middleware.NewResponseRecorder(writer)
			want := http.StatusOK
			if mode == "explicit-status" {
				want = http.StatusAccepted
				rec.WriteHeader(want)
			}
			native.before = func() {
				if !rec.WroteHeader() {
					t.Error("supported flush did not latch before actual operation")
				}
				for _, r := range nested {
					if !r.WroteHeader() {
						t.Error("nested supported flush did not latch before operation")
					}
				}
			}
			err := http.NewResponseController(rec).Flush()
			if err != native.err || native.calls != 1 || both.voidCalls != 0 {
				t.Fatal("native flush identity/count/preference lost", err, native.calls, both.voidCalls)
			}
			if !rec.WroteHeader() || rec.Status() != want || inner.Code != want || !inner.Flushed {
				t.Fatal("native flush/status semantics changed")
			}
			for _, r := range nested {
				if !r.WroteHeader() || r.Status() != want {
					t.Fatal("nested recorder did not retain committed status")
				}
			}
		})
	}
}

func TestResponseRecorder_ControllerUnsupportedDoesNotLatch(t *testing.T) {
	for _, mode := range []string{"nil", "plain", "unwrap-only", "nested"} {
		t.Run(mode, func(t *testing.T) {
			var writer http.ResponseWriter = &nonFlusher{}
			if mode == "nil" {
				writer = nil
			}
			if mode == "unwrap-only" {
				writer = unwrapOnlyWriter{ResponseWriter: writer}
			}
			var nested *middleware.ResponseRecorder
			if mode == "nested" {
				nested = middleware.NewResponseRecorder(writer)
				writer = nested
			}
			rec := middleware.NewResponseRecorder(writer)
			if err := http.NewResponseController(rec).Flush(); !errors.Is(err, http.ErrNotSupported) {
				t.Fatal("unsupported flush falsely succeeded", err)
			}
			if rec.WroteHeader() || nested != nil && nested.WroteHeader() {
				t.Fatal("unsupported flush latched a response")
			}
			rec.Flush() // The old void API remains a harmless unsupported no-op.
			if rec.WroteHeader() {
				t.Fatal("legacy unsupported flush latched a response")
			}
		})
	}
}

func TestResponseRecorder_LegacyFlushRetainsNativeOperation(t *testing.T) {
	inner := httptest.NewRecorder()
	native := &bothFlushWriter{errorFlushWriter: &errorFlushWriter{ResponseWriter: inner, inner: inner, err: errors.New("invented native flush failure")}}
	rec := middleware.NewResponseRecorder(native)
	rec.Flush()
	if native.calls != 1 || native.voidCalls != 0 || !rec.WroteHeader() || !inner.Flushed {
		t.Fatal("legacy Flush did not retain the native operation")
	}
}

func TestResponseRecorder_DefaultNativeStackPreservesFlushError(t *testing.T) {
	for _, failure := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "failure"}[failure], func(t *testing.T) {
			inner := httptest.NewRecorder()
			native := &bothFlushWriter{errorFlushWriter: &errorFlushWriter{ResponseWriter: inner, inner: inner}}
			if failure {
				native.err = errors.New("invented native ACK flush failure")
			}
			var actual error
			h := stack.Default(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusAccepted)
				actual = http.NewResponseController(w).Flush()
			}), slog.New(slog.NewTextHandler(io.Discard, nil)), stack.WithoutTimeout(), stack.WithMetricsOptions(metrics.WithRegisterer(prometheus.NewRegistry())))
			// Only the buffering timeout is disabled for this native flush test.
			// Real recovery/metrics/tracing/access logging remain in the actual chain.
			h.ServeHTTP(native, httptest.NewRequest(http.MethodPost, "/internal/v1/organization-events", nil))
			if actual != native.err || native.calls != 1 || native.voidCalls != 0 {
				t.Fatal("actual maintained stack hid the native flush error", actual, native.calls, native.voidCalls)
			}
			if inner.Code != http.StatusAccepted || !inner.Flushed {
				t.Fatal("actual stack changed committed status")
			}
		})
	}
}

type functionalFlushWriter struct {
	*errorFlushWriter
	unwrapped http.ResponseWriter
}

func (w *functionalFlushWriter) Unwrap() http.ResponseWriter { return w.unwrapped }

func TestResponseRecorder_PrefersFunctionalFlushErrorBeforeUnwrap(t *testing.T) {
	for _, leaf := range []http.ResponseWriter{nil, &nonFlusher{}} {
		inner := httptest.NewRecorder()
		native := &errorFlushWriter{ResponseWriter: inner, inner: inner, err: errors.New("invented functional wrapper flush error")}
		rec := middleware.NewResponseRecorder(&functionalFlushWriter{errorFlushWriter: native, unwrapped: leaf})
		if err := http.NewResponseController(rec).Flush(); err != native.err || native.calls != 1 || !rec.WroteHeader() {
			t.Fatal("functional FlushError was skipped through Unwrap", err, native.calls)
		}
	}
}

type functionalVoidFlushWriter struct {
	http.ResponseWriter
	inner *httptest.ResponseRecorder
	calls int
}

func (w *functionalVoidFlushWriter) Flush()                      { w.calls++; w.inner.Flush() }
func (w *functionalVoidFlushWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func TestResponseRecorder_PreservesFunctionalVoidFlushWithUnwrap(t *testing.T) {
	inner := httptest.NewRecorder()
	native := &functionalVoidFlushWriter{ResponseWriter: &nonFlusher{}, inner: inner}
	rec := middleware.NewResponseRecorder(native)
	if err := http.NewResponseController(rec).Flush(); err != nil || native.calls != 1 || !inner.Flushed || !rec.WroteHeader() {
		t.Fatal("functional void flush was skipped for Unwrap", err, native.calls)
	}
}

func TestResponseRecorder_FlushDoesNotCrossTimeoutBarrier(t *testing.T) {
	inner := httptest.NewRecorder()
	native := &bothFlushWriter{errorFlushWriter: &errorFlushWriter{ResponseWriter: inner, inner: inner}}
	var actual error
	var started bool
	h := timeout.Timeout(time.Second)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		rec := middleware.NewResponseRecorder(w)
		actual = http.NewResponseController(rec).Flush()
		started = rec.WroteHeader()
	}))
	h.ServeHTTP(native, httptest.NewRequest(http.MethodPost, "/fixture", nil))
	if !errors.Is(actual, http.ErrNotSupported) || started || native.calls != 0 || native.voidCalls != 0 {
		t.Fatal("buffering timeout falsely advertised native flush", actual, native.calls)
	}
}

func TestResponseRecorder_SupportedFlushPanicAlreadyMarksStarted(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusAccepted} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			inner := httptest.NewRecorder()
			native := &errorFlushWriter{ResponseWriter: inner, inner: inner}
			rec := middleware.NewResponseRecorder(native)
			if status != http.StatusOK {
				rec.WriteHeader(status)
			}
			marker := new(struct{})
			native.before = func() { inner.Flush(); panic(marker) }
			defer func() {
				if recover() != marker || !rec.WroteHeader() || rec.Status() != status || inner.Code != status || native.calls != 1 {
					t.Fatal("supported panic lost response-start/status evidence")
				}
			}()
			_ = http.NewResponseController(rec).Flush()
			t.Fatal("native panic was swallowed")
		})
	}
}

func TestResponseRecorder_Hijack_NotSupported(t *testing.T) {
	rec := middleware.NewResponseRecorder(httptest.NewRecorder())
	_, _, err := rec.Hijack()
	if err == nil {
		t.Error("expected error for non-hijackable writer")
	}
}

func TestResponseRecorder_Unwrap(t *testing.T) {
	inner := httptest.NewRecorder()
	rec := middleware.NewResponseRecorder(inner)
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
	rec := middleware.NewResponseRecorder(inner)

	if err := rec.Push("/asset.js", nil); err != nil {
		t.Fatalf("Push returned error: %v", err)
	}
	if inner.target != "/asset.js" {
		t.Fatalf("target = %q, want /asset.js", inner.target)
	}
}

func TestResponseRecorder_PushUnsupported(t *testing.T) {
	rec := middleware.NewResponseRecorder(httptest.NewRecorder())
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
	rec := middleware.NewResponseRecorder(inner)

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
	rec := middleware.NewResponseRecorder(inner)

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
