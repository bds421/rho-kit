package lifecycle

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"testing"
	"time"
)

func TestNewHTTPServerOnServesOnTheGivenListener(t *testing.T) {
	t.Chdir(t.TempDir()) // a socket path is short on every system
	sock := "s.sock"
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{
		Handler:           http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ok") }),
		ReadHeaderTimeout: time.Second,
	}
	c := NewHTTPServerOn(srv, l)
	done := make(chan error, 1)
	go func() { done <- c.Start(context.Background()) }()

	client := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", sock)
	}}}
	resp, err := client.Get("http://unix/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if string(body) != "ok" {
		t.Fatalf("body %q", body)
	}
	if err := c.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := <-done; !errors.Is(err, http.ErrServerClosed) {
		t.Fatalf("Start returned %v", err)
	}
}

func TestNewHTTPServerOnRejectsMissingParts(t *testing.T) {
	srv := &http.Server{Handler: http.NotFoundHandler(), ReadHeaderTimeout: time.Second}
	for name, f := range map[string]func(){
		"nil listener": func() { NewHTTPServerOn(srv, nil) },
		"nil server":   func() { NewHTTPServerOn(nil, &net.UnixListener{}) },
		"no timeout":   func() { NewHTTPServerOn(&http.Server{Handler: http.NotFoundHandler()}, &net.UnixListener{}) },
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
