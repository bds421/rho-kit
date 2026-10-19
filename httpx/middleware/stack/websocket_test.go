package stack

import (
	"bufio"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

func hijackable(w http.ResponseWriter, _ *http.Request) {
	if _, ok := w.(http.Hijacker); !ok {
		http.Error(w, "not hijackable", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusSwitchingProtocols)
}

func upgrade(path string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, path, nil)
	r.Header.Set("Connection", "Upgrade")
	r.Header.Set("Upgrade", "websocket")
	return r
}

func TestWebSocketPathsBypassTheBufferingTimeout(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := Default(http.HandlerFunc(hijackable), logger, WithoutMetrics(), WithWebSocketPaths("/connection/websocket"))

	cases := []struct {
		name string
		req  *http.Request
		want int
	}{
		{"upgrade on a named path", upgrade("/connection/websocket"), http.StatusSwitchingProtocols},
		{"upgrade on another path", upgrade("/api/orders"), http.StatusInternalServerError},
		{"plain request on a named path", httptest.NewRequest(http.MethodGet, "/connection/websocket", nil), http.StatusInternalServerError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := &hijackRecorder{httptest.NewRecorder()}
			h.ServeHTTP(rec, tc.req)
			if rec.Code != tc.want {
				t.Fatalf("status %d, want %d", rec.Code, tc.want)
			}
		})
	}
}

func TestWithWebSocketPathsRejectsRelativePaths(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("relative path accepted")
		}
	}()
	WithWebSocketPaths("connection")
}

type hijackRecorder struct{ *httptest.ResponseRecorder }

func (hijackRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) { return nil, nil, nil }
