package healthhttp

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/bds421/rho-kit/observability/v2/health"
)

func TestHandler_PanicsOnNilChecker(t *testing.T) {
	defer func() {
		if rcv := recover(); rcv == nil {
			t.Fatal("expected panic when Handler called with nil *health.Checker")
		}
	}()
	_ = Handler(nil)
}

func TestHandler_PanicsOnInvalidChecker(t *testing.T) {
	assert.PanicsWithValue(t, "healthhttp: Handler requires a valid *health.Checker", func() {
		_ = Handler(&health.Checker{
			Checks: []health.DependencyCheck{{Name: "secret-token"}},
		})
	})
}

func TestNewInternalHandler_PanicsOnNilReadiness(t *testing.T) {
	assert.Panics(t, func() {
		_ = NewInternalHandler("v1", nil)
	})
}

func TestNewInternalHandler_PanicsOnNilOption(t *testing.T) {
	assert.Panics(t, func() {
		_ = NewInternalHandler("v1", http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}), nil)
	})
}

func TestHTTPCheckReturnsValidDependencyCheck(t *testing.T) {
	client := &http.Client{}
	check := HTTPCheck("upstream-api", "http://127.0.0.1", client)
	assert.NoError(t, health.ValidateDependencyCheck(check))
}

func TestHTTPCheck_PanicsOnInvalidName(t *testing.T) {
	assert.Panics(t, func() {
		_ = HTTPCheck("Bad Name!", "http://127.0.0.1", &http.Client{})
	})
}

func TestHTTPCheck_PanicsOnEmptyURL(t *testing.T) {
	assert.Panics(t, func() {
		_ = HTTPCheck("upstream-api", "", &http.Client{})
	})
}

func TestHTTPCheck_PanicsOnMalformedURL(t *testing.T) {
	assert.Panics(t, func() {
		_ = HTTPCheck("upstream-api", "://missing-scheme", &http.Client{})
	})
}

func TestCriticalHTTPCheck_PanicsOnInvalidName(t *testing.T) {
	assert.Panics(t, func() {
		_ = CriticalHTTPCheck("Bad Name!", "http://127.0.0.1", &http.Client{})
	})
}

func TestHTTPCheck_DrainsResponseBodyWithByteCap(t *testing.T) {
	// A dependency that streams forever must not block the probe on the
	// body drain: the check caps the drain at a small fixed limit.
	read := make(chan struct{}, 1)
	transport := healthRoundTripper(func(_ *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(infiniteReader{read: read}),
		}, nil
	})
	check := HTTPCheck("upstream-api", "http://127.0.0.1", &http.Client{
		Timeout:       time.Second,
		Transport:     transport,
		CheckRedirect: blockHTTPCheckRedirect,
	})

	done := make(chan string, 1)
	go func() { done <- check.Check(t.Context()) }()

	select {
	case got := <-done:
		if got != health.StatusHealthy {
			t.Fatalf("status = %q, want %q", got, health.StatusHealthy)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("HTTPCheck did not cap the body drain: probe hung on an infinite body")
	}
}

// infiniteReader produces bytes forever, modelling a dependency that streams
// without end. It records that it was read from at least once.
type infiniteReader struct {
	read chan<- struct{}
}

func (r infiniteReader) Read(p []byte) (int, error) {
	select {
	case r.read <- struct{}{}:
	default:
	}
	for i := range p {
		p[i] = 'x'
	}
	return len(p), nil
}

func TestDependencyHTTPClient_FillsSafeDefaultsWithoutMutatingCaller(t *testing.T) {
	caller := &http.Client{}

	client := dependencyHTTPClient(caller)
	if client == caller {
		t.Fatal("expected client without timeout/redirect policy to be cloned")
	}
	if caller.Timeout != 0 {
		t.Fatalf("caller timeout mutated to %s", caller.Timeout)
	}
	if caller.CheckRedirect != nil {
		t.Fatal("caller redirect policy mutated")
	}
	if client.Timeout != 5*time.Second {
		t.Fatalf("timeout = %s, want 5s", client.Timeout)
	}

	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/next", http.StatusFound)
	}))
	defer redirector.Close()

	resp, err := client.Get(redirector.URL)
	if resp != nil {
		_ = resp.Body.Close()
	}
	if !errors.Is(err, errHTTPCheckRedirectBlocked) {
		t.Fatalf("Get redirect error = %v, want errHTTPCheckRedirectBlocked", err)
	}
}

func TestDependencyHTTPClient_PreservesExplicitRedirectPolicy(t *testing.T) {
	transport := healthRoundTripper(func(_ *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody}, nil
	})
	caller := &http.Client{
		Timeout:   2 * time.Second,
		Transport: transport,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	client := dependencyHTTPClient(caller)
	if client != caller {
		t.Fatal("expected fully specified client to be preserved")
	}
}

func TestDependencyHTTPClient_FillsTransportWhenDefaultTransportReplaced(t *testing.T) {
	prev := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = prev })
	http.DefaultTransport = healthRoundTripper(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("global default transport used")
	})

	client := dependencyHTTPClient(&http.Client{Timeout: time.Second, CheckRedirect: blockHTTPCheckRedirect})
	if client.Transport == nil {
		t.Fatal("expected transport to be filled")
	}
	if _, ok := client.Transport.(*http.Transport); !ok {
		t.Fatalf("transport = %T, want *http.Transport fallback", client.Transport)
	}
}

func TestHTTPCheckBlocksRedirectsByDefault(t *testing.T) {
	redirectTargetHit := false
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirectTargetHit = true
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer redirector.Close()

	check := HTTPCheck("upstream-api", redirector.URL, &http.Client{})
	got := check.Check(t.Context())
	if got != health.StatusUnhealthy {
		t.Fatalf("status = %q, want %q", got, health.StatusUnhealthy)
	}
	if redirectTargetHit {
		t.Fatal("HTTPCheck followed a dependency redirect")
	}
}

type healthRoundTripper func(*http.Request) (*http.Response, error)

func (f healthRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestHTTPCheck_PanicsOnSchemeLessURL(t *testing.T) {
	assert.Panics(t, func() {
		_ = HTTPCheck("upstream-api", "/ready", &http.Client{})
	})
	assert.Panics(t, func() {
		_ = HTTPCheck("upstream-api", "ftp://example.com/ready", &http.Client{})
	})
}
