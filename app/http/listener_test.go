package http_test

import (
	"context"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/bds421/rho-kit/app/v2"
	apphttp "github.com/bds421/rho-kit/app/http/v2"
)

func unixClient(sock string) *http.Client {
	return &http.Client{Timeout: time.Second, Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", sock)
	}}}
}

// A service handed listeners serves its API and its ops endpoints on them,
// without ports and without binding an address.
func TestTheServersServeOnGivenListeners(t *testing.T) {
	t.Chdir(t.TempDir()) // socket paths are short on every system
	public, err := net.Listen("unix", "api.sock")
	if err != nil {
		t.Fatal(err)
	}
	internal, err := net.Listen("unix", "ops.sock")
	if err != nil {
		t.Fatal(err)
	}
	b := app.New("svc", "v0.0.1", app.BaseConfig{}).
		WithoutRateLimit().
		With(apphttp.Module(apphttp.WithoutTLS(), apphttp.WithListener(public), apphttp.WithInternalListener(internal))).
		Router(func(app.Infrastructure) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "api") })
		})
	if err := b.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- b.RunContext(ctx) }()

	get := func(sock, path string) (int, string) {
		t.Helper()
		var lastErr error
		for range 100 {
			resp, err := unixClient(sock).Get("http://svc" + path)
			if err == nil {
				body, _ := io.ReadAll(resp.Body)
				_ = resp.Body.Close()
				return resp.StatusCode, string(body)
			}
			lastErr = err
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatalf("%s%s: %v", sock, path, lastErr)
		return 0, ""
	}
	if code, body := get("api.sock", "/x"); code != http.StatusOK || body != "api" {
		t.Errorf("public: %d %q", code, body)
	}
	if code, _ := get("ops.sock", "/ready"); code != http.StatusOK {
		t.Errorf("ready: %d", code)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return")
	}
}

func TestListenerOptionsRejectNil(t *testing.T) {
	for name, f := range map[string]func(){
		"public":   func() { apphttp.WithListener(nil) },
		"internal": func() { apphttp.WithInternalListener(nil) },
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("%s: no panic", name)
				}
			}()
			f()
		}()
	}
}
