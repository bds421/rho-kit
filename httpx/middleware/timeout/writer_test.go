package timeout

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestTimeout_WriteHeaderContract verifies the buffered timeoutWriter mirrors
// the http.ResponseWriter contract enforced by the stdlib:
//   - the first WriteHeader (or implicit WriteHeader(200) from Write) latches
//     the final status; later WriteHeader calls are superfluous no-ops;
//   - 1xx informational codes never become the final flushed status.
func TestTimeout_WriteHeaderContract(t *testing.T) {
	tests := []struct {
		name     string
		handler  http.HandlerFunc
		wantCode int
		wantBody string
	}{
		{
			name: "double WriteHeader keeps first",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusCreated)
				w.WriteHeader(http.StatusInternalServerError)
			},
			wantCode: http.StatusCreated,
		},
		{
			name: "Write then WriteHeader keeps implicit 200",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte("body"))
				w.WriteHeader(http.StatusInternalServerError)
			},
			wantCode: http.StatusOK,
			wantBody: "body",
		},
		{
			name: "1xx informational code is not flushed as final status",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusEarlyHints) // 103
				_, _ = w.Write([]byte("real body"))
			},
			wantCode: http.StatusOK,
			wantBody: "real body",
		},
		{
			name: "1xx then explicit final status uses final",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusEarlyHints) // 103
				w.WriteHeader(http.StatusAccepted)   // 202
				_, _ = w.Write([]byte("ok"))
			},
			wantCode: http.StatusAccepted,
			wantBody: "ok",
		},
		{
			name: "explicit status then Write keeps explicit status",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusCreated)
				_, _ = w.Write([]byte("created"))
			},
			wantCode: http.StatusCreated,
			wantBody: "created",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := Timeout(5 * time.Second)(http.HandlerFunc(tt.handler))

			req := httptest.NewRequest(http.MethodGet, "/", nil)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != tt.wantCode {
				t.Errorf("status = %d, want %d", rec.Code, tt.wantCode)
			}
			if got := rec.Body.String(); got != tt.wantBody {
				t.Errorf("body = %q, want %q", got, tt.wantBody)
			}
		})
	}
}

func TestTimeout_BufferLimitIsAtomic(t *testing.T) {
	for _, tc := range []struct {
		name       string
		chunks     []string
		wantStatus int
	}{
		{"exact limit and empty write", []string{"12345678", ""}, http.StatusCreated},
		{"limit plus one", []string{"123456789"}, http.StatusInternalServerError},
		{"ignored partial writer error", []string{"1234", "56789", "x"}, http.StatusInternalServerError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := Timeout(time.Second, WithMaxBufferSize(8))(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Length", "8")
				w.Header().Set("ETag", "old")
				w.Header().Set("Content-Encoding", "gzip")
				w.Header().Set("Content-Range", "bytes 0-8/9")
				w.Header().Set("Set-Cookie", "partial=bad")
				w.Header().Set("Trailer", "X-Partial")
				w.WriteHeader(http.StatusCreated)
				accepted := 0
				failed := false
				for _, chunk := range tc.chunks {
					n, err := w.Write([]byte(chunk))
					if failed || len(chunk) > 8-accepted {
						failed = true
						if n != 0 || !errors.Is(err, ErrResponseTooLarge) {
							t.Errorf("overflow Write = (%d, %v), want (0, ErrResponseTooLarge)", n, err)
						}
					} else {
						accepted += len(chunk)
						if n != len(chunk) || err != nil {
							t.Errorf("bounded Write = (%d, %v)", n, err)
						}
					}
				}
			}))
			rec := httptest.NewRecorder()
			rec.Header().Set("X-Trace-ID", "kept")
			rec.Header().Set("Strict-Transport-Security", "max-age=123")
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tc.wantStatus)
			}
			if tc.wantStatus == http.StatusCreated {
				if rec.Body.String() != "12345678" {
					t.Fatalf("bounded body = %q", rec.Body.String())
				}
				return
			}
			if rec.Body.String() != "{\"error\":\"internal error\",\"code\":\"INTERNAL\"}\n" {
				t.Errorf("overflow body = %q", rec.Body.String())
			}
			for _, name := range []string{"Content-Length", "ETag", "Content-Encoding", "Content-Range", "Set-Cookie", "Trailer"} {
				if rec.Header().Get(name) != "" {
					t.Errorf("stale %s survived", name)
				}
			}
			if rec.Header().Get("Content-Type") != "application/json" || rec.Header().Get("Cache-Control") != "no-store" {
				t.Error("missing canonical error headers")
			}
			if rec.Header().Get("X-Trace-ID") != "kept" || rec.Header().Get("Strict-Transport-Security") != "max-age=123" {
				t.Error("outer security/tracing headers lost")
			}
		})
	}
}

func TestTimeout_OverflowHEADDiscardsRepresentation(t *testing.T) {
	h := Timeout(time.Second, WithMaxBufferSize(8))(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("ETag", "handler-value")
		_, _ = w.Write([]byte("123456789"))
	}))
	rec := httptest.NewRecorder()
	// Representation metadata may already have been set by an outer wrapper.
	rec.Header().Set("Content-Length", "9")
	rec.Header()["ETag"] = []string{"outer-value"}
	rec.Header().Set("Content-Encoding", "gzip")
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodHead, "/", nil))
	if rec.Code != http.StatusInternalServerError || rec.Body.Len() != 0 {
		t.Fatalf("HEAD overflow = %d, %q", rec.Code, rec.Body.String())
	}
	for _, name := range []string{"Content-Length", "ETag", "Content-Encoding"} {
		if rec.Header().Get(name) != "" || len(rec.Header()[name]) != 0 {
			t.Errorf("HEAD stale %s survived", name)
		}
	}
}
