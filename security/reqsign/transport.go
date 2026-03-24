package reqsign

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
)

// maxSignBodySize is the maximum request body size the transport will buffer
// for signing (1 MiB), matching the middleware limit.
const maxSignBodySize = 1 << 20

// SigningTransport wraps an http.RoundTripper to sign all outbound requests.
type SigningTransport struct {
	base  http.RoundTripper
	store KeyStore
	opts  []SignOption
}

// NewSigningTransport creates a transport that signs every outbound request.
// If base is nil, http.DefaultTransport is used.
func NewSigningTransport(base http.RoundTripper, store KeyStore, opts ...SignOption) *SigningTransport {
	if base == nil {
		base = http.DefaultTransport
	}
	return &SigningTransport{
		base:  base,
		store: store,
		opts:  opts,
	}
}

// RoundTrip reads the request body (if present), signs the request, and
// delegates to the wrapped transport. The body is replaced with a new reader
// so downstream can still read it.
func (t *SigningTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	var body []byte

	if req.Body != nil && req.Body != http.NoBody {
		var err error
		body, err = io.ReadAll(io.LimitReader(req.Body, maxSignBodySize+1))
		if err != nil {
			return nil, fmt.Errorf("reqsign: reading request body: %w", err)
		}
		if int64(len(body)) > maxSignBodySize {
			return nil, fmt.Errorf("reqsign: request body exceeds %d bytes", maxSignBodySize)
		}
		if err := req.Body.Close(); err != nil {
			return nil, fmt.Errorf("reqsign: closing request body: %w", err)
		}
		req.Body = io.NopCloser(bytes.NewReader(body))
	}

	if err := SignRequest(req, body, t.store, t.opts...); err != nil {
		return nil, err
	}

	return t.base.RoundTrip(req)
}
