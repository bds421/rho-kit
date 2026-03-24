package reqsign

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
)

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

// RoundTrip clones the request, reads the body (if present), signs the clone,
// and delegates to the wrapped transport. The original request is never mutated,
// in accordance with the http.RoundTripper contract.
func (t *SigningTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())

	var body []byte

	if clone.Body != nil && clone.Body != http.NoBody {
		// Close the original cloned body after reading; clone.Body is
		// replaced with a fresh NopCloser reader below before RoundTrip.
		defer func() { _ = clone.Body.Close() }()
		var err error
		body, err = io.ReadAll(io.LimitReader(clone.Body, MaxBodySize+1))
		if err != nil {
			return nil, fmt.Errorf("reqsign: reading request body: %w", err)
		}
		if len(body) > MaxBodySize {
			return nil, fmt.Errorf("reqsign: request body exceeds %d bytes", MaxBodySize)
		}
		clone.Body = io.NopCloser(bytes.NewReader(body))
	}

	if err := SignRequest(clone, body, t.store, t.opts...); err != nil {
		return nil, err
	}

	return t.base.RoundTrip(clone)
}
