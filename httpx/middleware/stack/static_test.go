package stack

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestStaticPathsStreamPastTheTimeoutBuffer(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	large := bytes.Repeat([]byte("a"), 2<<20)
	h := Default(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(large)
	}), logger, WithoutMetrics(), WithStaticPaths("/assets/"))

	cases := []struct {
		name   string
		method string
		path   string
		want   int
	}{
		{"read under a static prefix", http.MethodGet, "/assets/app.js", http.StatusOK},
		{"head under a static prefix", http.MethodHead, "/assets/app.js", http.StatusOK},
		{"write under a static prefix", http.MethodPost, "/assets/app.js", http.StatusInternalServerError},
		{"read elsewhere", http.MethodGet, "/api/export", http.StatusInternalServerError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, nil))
			if rec.Code != tc.want {
				t.Fatalf("status %d, want %d", rec.Code, tc.want)
			}
		})
	}
}

func TestWithStaticPathsRejectsTheRootAndRelativePrefixes(t *testing.T) {
	for _, prefix := range []string{"assets/", "/", ""} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("prefix %q accepted", prefix)
				}
			}()
			WithStaticPaths(prefix)
		}()
	}
}
