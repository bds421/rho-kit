package gcsbackend

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	gcsstorage "cloud.google.com/go/storage"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"google.golang.org/api/option"

	"github.com/bds421/rho-kit/infra/v2/storage"
)

// newTestBackend wires a Backend to a custom GCS client pointed at the
// given httptest server, with its own private registry so error-metric
// assertions are isolated.
func newTestBackend(t *testing.T, srv *httptest.Server) (*Backend, *prometheus.Registry) {
	t.Helper()

	client, err := gcsstorage.NewClient(
		context.Background(),
		option.WithEndpoint(srv.URL),
		option.WithHTTPClient(srv.Client()),
		option.WithoutAuthentication(),
	)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	reg := prometheus.NewRegistry()
	b := NewWithClient(client, Config{Bucket: "test-bucket"}, WithMetricsRegisterer(reg))
	return b, reg
}

// attrsJSON is a minimal object metadata response for the JSON API so the
// initial obj.Attrs call in Get succeeds.
const attrsJSON = `{
  "kind": "storage#object",
  "bucket": "test-bucket",
  "name": "key",
  "generation": "1700000000000001",
  "contentType": "text/plain",
  "size": "5",
  "etag": "abc"
}`

// TestGet_NewReaderNotFound covers the Attrs->NewReader race: the object
// exists for the metadata fetch but is deleted (or its pinned generation
// removed) before the download, so NewReader returns ErrObjectNotExist.
// That must map to storage.ErrObjectNotFound and must not inflate
// operation_errors_total — the same contract Delete/Exists honour.
func TestGet_NewReaderNotFound(t *testing.T) {
	cases := []struct {
		name           string
		downloadStatus int
	}{
		// 404 on the media GET is how the GCS SDK signals the object (or
		// the pinned generation) vanished between Attrs and NewReader.
		{name: "deleted in window", downloadStatus: http.StatusNotFound},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// JSON metadata API (endpoint-relative): GET /b/<bucket>/o/<object>.
				// This must succeed so Get reaches the NewReader call.
				if strings.HasPrefix(r.URL.Path, "/b/") && strings.Contains(r.URL.Path, "/o/") {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusOK)
					_, _ = io.WriteString(w, attrsJSON)
					return
				}
				// Media download path: GET /<bucket>/<object>?generation=...
				// Simulate the object disappearing in the Attrs->NewReader window.
				w.WriteHeader(tc.downloadStatus)
			}))
			defer srv.Close()

			b, _ := newTestBackend(t, srv)

			rc, _, err := b.Get(context.Background(), "key")
			if rc != nil {
				_ = rc.Close()
			}
			if err == nil {
				t.Fatal("Get error = nil, want not-found")
			}
			if !errors.Is(err, storage.ErrObjectNotFound) {
				t.Fatalf("Get error = %v, want errors.Is ErrObjectNotFound", err)
			}

			if got := testutil.ToFloat64(b.metrics.opErrors.WithLabelValues(b.instance, "get")); got != 0 {
				t.Fatalf("get operation_errors_total = %v, want 0 (not-found must not inflate errors)", got)
			}
		})
	}
}
