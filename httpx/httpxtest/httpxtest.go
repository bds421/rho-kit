// Package httpxtest provides test helpers for HTTP handler testing.
// It simplifies the common pattern of building requests, recording responses,
// and asserting on status codes and JSON bodies.
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
