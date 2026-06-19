package timeout

import (
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
