package webapp

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
)

func build() fstest.MapFS {
	return fstest.MapFS{
		"index.html":        {Data: []byte("app")},
		"index.html.br":     {Data: []byte("app-br")},
		"assets/app.js":     {Data: []byte("js")},
		"assets/app.js.br":  {Data: []byte("js-br")},
		"assets/app.js.gz":  {Data: []byte("js-gz")},
		"assets/logo.woff2": {Data: []byte("font")},
		"favicon.svg":       {Data: []byte("svg")},
	}
}

func serve(t *testing.T, h http.Handler, method, target string, header http.Header) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, nil)
	for k, v := range header {
		req.Header[k] = v
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func newHandler(t *testing.T) http.Handler {
	t.Helper()
	h, err := New(build(), WithImmutable("assets/"))
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestServesFilesAndTheAppForEveryOtherPath(t *testing.T) {
	h := newHandler(t)
	cases := []struct {
		path, body, cache, contentType string
	}{
		{"/", "app", "no-cache", "text/html; charset=utf-8"},
		{"/index.html", "app", "no-cache", "text/html; charset=utf-8"},
		{"/o/123/workspaces", "app", "no-cache", "text/html; charset=utf-8"},
		{"/assets/app.js", "js", "public, max-age=31536000, immutable", "text/javascript; charset=utf-8"},
		{"/assets/logo.woff2", "font", "public, max-age=31536000, immutable", "font/woff2"},
		{"/favicon.svg", "svg", "no-cache", "image/svg+xml"},
	}
	for _, c := range cases {
		rec := serve(t, h, http.MethodGet, c.path, nil)
		if rec.Code != http.StatusOK || rec.Body.String() != c.body {
			t.Errorf("%s: %d %q, want %q", c.path, rec.Code, rec.Body.String(), c.body)
		}
		if got := rec.Header().Get("Cache-Control"); got != c.cache {
			t.Errorf("%s: Cache-Control %q, want %q", c.path, got, c.cache)
		}
		if got := rec.Header().Get("Content-Type"); got != c.contentType {
			t.Errorf("%s: Content-Type %q, want %q", c.path, got, c.contentType)
		}
	}
}

func TestSendsThePrecompressedFileTheClientAccepts(t *testing.T) {
	h := newHandler(t)
	cases := []struct {
		accept, body, coding string
	}{
		{"gzip, deflate, br, zstd", "js-br", "br"},
		{"gzip", "js-gz", "gzip"},
		{"br;q=0, gzip", "js-gz", "gzip"},
		{"", "js", ""},
		{"identity", "js", ""},
	}
	for _, c := range cases {
		rec := serve(t, h, http.MethodGet, "/assets/app.js", http.Header{"Accept-Encoding": {c.accept}})
		if rec.Body.String() != c.body || rec.Header().Get("Content-Encoding") != c.coding {
			t.Errorf("%q: body %q coding %q, want %q %q", c.accept, rec.Body.String(), rec.Header().Get("Content-Encoding"), c.body, c.coding)
		}
		if rec.Header().Get("Vary") != "Accept-Encoding" {
			t.Errorf("%q: Vary %q", c.accept, rec.Header().Get("Vary"))
		}
		if rec.Header().Get("Content-Type") != "text/javascript; charset=utf-8" {
			t.Errorf("%q: Content-Type %q", c.accept, rec.Header().Get("Content-Type"))
		}
	}
	if rec := serve(t, h, http.MethodGet, "/o/1", http.Header{"Accept-Encoding": {"br"}}); rec.Body.String() != "app-br" {
		t.Errorf("app path: body %q, want the precompressed index", rec.Body.String())
	}
	if rec := serve(t, h, http.MethodGet, "/favicon.svg", http.Header{"Accept-Encoding": {"br"}}); rec.Header().Get("Vary") != "" {
		t.Errorf("a file without siblings varies by %q", rec.Header().Get("Vary"))
	}
}

func TestMissingHashedFileIsNotFound(t *testing.T) {
	h := newHandler(t)
	for _, p := range []string{"/assets/gone.js", "/assets/app.js.br", "/assets/"} {
		if rec := serve(t, h, http.MethodGet, p, nil); rec.Code != http.StatusNotFound {
			t.Errorf("%s: status %d, want 404", p, rec.Code)
		}
	}
	if rec := serve(t, h, http.MethodGet, "/index.html.br", nil); rec.Body.String() != "app" {
		t.Errorf("a sibling outside the hashed files is served as %q, want the app", rec.Body.String())
	}
}

func TestRevalidatesWithTheETag(t *testing.T) {
	h := newHandler(t)
	first := serve(t, h, http.MethodGet, "/", nil)
	etag := first.Header().Get("ETag")
	if etag == "" {
		t.Fatal("no ETag")
	}
	again := serve(t, h, http.MethodGet, "/", http.Header{"If-None-Match": {etag}})
	if again.Code != http.StatusNotModified {
		t.Errorf("status %d, want 304", again.Code)
	}
	br := serve(t, h, http.MethodGet, "/", http.Header{"Accept-Encoding": {"br"}})
	if br.Header().Get("ETag") == etag {
		t.Error("the brotli variant shares the plain file's ETag")
	}
}

func TestAnswersOnlyReads(t *testing.T) {
	h := newHandler(t)
	if rec := serve(t, h, http.MethodHead, "/assets/app.js", nil); rec.Code != http.StatusOK || rec.Body.Len() != 0 {
		t.Errorf("HEAD: %d with %d bytes", rec.Code, rec.Body.Len())
	}
	rec := serve(t, h, http.MethodPost, "/", nil)
	if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != "GET, HEAD" {
		t.Errorf("POST: %d Allow %q", rec.Code, rec.Header().Get("Allow"))
	}
}

func TestRefusesPathsOutsideTheBuild(t *testing.T) {
	rec := serve(t, newHandler(t), http.MethodGet, "/../../etc/passwd", nil)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status %d, want the request refused", rec.Code)
	}
}

func TestRequiresAnIndex(t *testing.T) {
	if _, err := New(fstest.MapFS{"assets/app.js": {Data: []byte("js")}}); err == nil {
		t.Error("a build without index.html was accepted")
	}
}
