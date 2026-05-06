// Package httpxtest provides test helpers for HTTP handler testing.
// It simplifies the common pattern of building requests, recording responses,
// and asserting on status codes and JSON bodies.
//
// IMPORTANT: [Do] and [DoRequest] call handler.ServeHTTP directly with
// httptest.NewRequest. They DO NOT exercise net/http's server logic —
// MaxHeaderBytes, ReadTimeout, RemoteAddr, default Host parsing, transfer
// encoding negotiation, and HTTP/2 behaviour are all bypassed. Middleware
// or handler code that depends on those will pass these tests but fail
// against a real server.
//
// Use [DoRealServer] when the handler relies on real-server behaviour:
// it spins up an [httptest.Server] (which IS a real *http.Server with a
// real socket pair) and dispatches the request through it.
package httpxtest

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Response wraps an httptest.ResponseRecorder with convenience methods.
type Response struct {
	*httptest.ResponseRecorder
}

// Do sends an HTTP request to handler and returns the recorded response.
// body may be nil for requests without a body.
func Do(t *testing.T, handler http.Handler, method, path string, body any) *Response {
	t.Helper()
	var bodyReader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("httpxtest: marshal body: %v", err)
		}
		bodyReader = bytes.NewReader(data)
	}
	req := httptest.NewRequest(method, path, bodyReader)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return &Response{rec}
}

// DoRequest sends a pre-built *http.Request to handler.
func DoRequest(handler http.Handler, req *http.Request) *Response {
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return &Response{rec}
}

// AssertStatus fails the test if the response status code doesn't match.
func (r *Response) AssertStatus(t *testing.T, expected int) {
	t.Helper()
	if r.Code != expected {
		t.Errorf("expected status %d, got %d; body: %s", expected, r.Code, r.Body.String())
	}
}

// AssertJSON fails the test if the status doesn't match, then unmarshals the
// response body into target.
func (r *Response) AssertJSON(t *testing.T, status int, target any) {
	t.Helper()
	r.AssertStatus(t, status)
	if err := json.NewDecoder(r.Body).Decode(target); err != nil {
		t.Fatalf("httpxtest: decode JSON response: %v", err)
	}
}

// BodyJSON unmarshals the response body into target without status assertion.
func (r *Response) BodyJSON(t *testing.T, target any) {
	t.Helper()
	if err := json.NewDecoder(r.Body).Decode(target); err != nil {
		t.Fatalf("httpxtest: decode JSON response: %v", err)
	}
}

// BodyString returns the response body as a string.
func (r *Response) BodyString() string {
	return r.Body.String()
}

// DoRealServer sends an HTTP request through a real [httptest.Server]
// fronting the handler and returns the response. Unlike [Do], this exercises
// net/http's actual server pipeline: MaxHeaderBytes, ReadTimeout, RemoteAddr,
// transfer encoding, and any other server-side behaviour the handler or
// middleware reads.
//
// Use when:
//   - middleware reads RemoteAddr (rate-limit, clientip, audit logging)
//   - middleware asserts MaxHeaderBytes / body-size limits
//   - tests need to verify behaviour under HTTP/2 or chunked transfer
//
// Slower than [Do] (one server start + dial per call). Prefer [Do] for
// pure handler/middleware unit tests where the server-level behaviour is
// not under test.
//
// body may be nil for requests without a body. handler-side overrides
// (HostHeader, Header.Set on req before sending) can be done by switching
// to [DoRealServerRequest], which accepts a pre-built *http.Request.
func DoRealServer(t *testing.T, handler http.Handler, method, path string, body any) *http.Response {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	var bodyReader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("httpxtest: marshal body: %v", err)
		}
		bodyReader = bytes.NewReader(data)
	}

	req, err := http.NewRequestWithContext(t.Context(), method, srv.URL+path, bodyReader)
	if err != nil {
		t.Fatalf("httpxtest: build request: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("httpxtest: dispatch real-server request: %v", err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

// DoRealServerRequest is the variant of [DoRealServer] that accepts a
// pre-built *http.Request, allowing the caller to set custom headers or
// a non-default Host. The request's URL is rewritten to point at the
// test server; the path, query, headers, and body are preserved.
func DoRealServerRequest(t *testing.T, handler http.Handler, req *http.Request) *http.Response {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	target, err := http.NewRequestWithContext(req.Context(), req.Method, srv.URL+req.URL.RequestURI(), req.Body)
	if err != nil {
		t.Fatalf("httpxtest: build request: %v", err)
	}
	target.Header = req.Header.Clone()
	if req.Host != "" {
		target.Host = req.Host
	}

	resp, err := srv.Client().Do(target)
	if err != nil {
		t.Fatalf("httpxtest: dispatch real-server request: %v", err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}
