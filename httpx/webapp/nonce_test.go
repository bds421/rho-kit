package webapp

import (
	"net/http"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/bds421/rho-kit/httpx/v2/middleware/cspnonce"
)

func TestWithCSPNonceGivesEachPageTheRequestsNonce(t *testing.T) {
	files := fstest.MapFS{
		"index.html":    {Data: []byte(`<html><head><base href="/"></head><body></body></html>`)},
		"index.html.br": {Data: []byte("br")},
		"assets/app.js": {Data: []byte("js")},
	}
	app, err := New(files, WithImmutable("assets/"), WithBasePath("/portal"), WithCSPNonce())
	if err != nil {
		t.Fatal(err)
	}
	h := cspnonce.Middleware()(app)
	for _, target := range []string{"/", "/settings", "/index.html"} {
		rec := serve(t, h, http.MethodGet, target, http.Header{"Accept-Encoding": {"br"}})
		policy := rec.Header().Get("Content-Security-Policy")
		nonce := strings.SplitN(strings.SplitN(policy, "'nonce-", 2)[1], "'", 2)[0]
		body := rec.Body.String()
		if !strings.Contains(body, `<base href="/portal/">`) || !strings.Contains(body, `<meta property="csp-nonce" nonce="`+nonce+`"></head>`) {
			t.Fatalf("%s: %s under %s", target, body, policy)
		}
		if rec.Header().Get("ETag") != "" || rec.Header().Get("Content-Encoding") != "" || rec.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("%s: cached or compressed: %v", target, rec.Header())
		}
	}
	if rec := serve(t, h, http.MethodGet, "/assets/app.js", nil); rec.Body.String() != "js" {
		t.Fatal(rec.Body.String())
	}
}

func TestWithCSPNonceWithoutTheMiddlewareServesThePage(t *testing.T) {
	app, err := New(fstest.MapFS{"index.html": {Data: []byte("<head></head>")}}, WithCSPNonce())
	if err != nil {
		t.Fatal(err)
	}
	if rec := serve(t, app, http.MethodGet, "/", nil); rec.Body.String() != "<head></head>" {
		t.Fatal(rec.Body.String())
	}
}

func TestWithCSPNonceRequiresAHead(t *testing.T) {
	if _, err := New(fstest.MapFS{"index.html": {Data: []byte("app")}}, WithCSPNonce()); err == nil {
		t.Fatal("an index without </head> was accepted")
	}
}

