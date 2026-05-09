package reqsign

import (
	"bytes"
	"fmt"
	"io"
	"net/http"

	"github.com/bds421/rho-kit/crypto/v2/signing"
)

// SigningTransport wraps an http.RoundTripper to sign all outbound requests.
type SigningTransport struct {
	base        http.RoundTripper
	store       signing.KeyStore
	opts        []SignOption
	maxBodySize int64
}

// NewSigningTransport creates a transport that signs every outbound request.
// If base is nil, http.DefaultTransport is used.
func NewSigningTransport(base http.RoundTripper, store signing.KeyStore, opts ...SignOption) *SigningTransport {
	if store == nil {
		panic(nilKeyStoreMsg)
	}
	if base == nil {
		base = http.DefaultTransport
	}

	// Pre-apply options to determine maxBodySize.
	cfg := signConfig{
		signer:      defaultSigner,
		maxBodySize: MaxBodySize,
	}
	for _, o := range opts {
		o(&cfg)
	}

	return &SigningTransport{
		base:        base,
		store:       store,
		opts:        opts,
		maxBodySize: cfg.maxBodySize,
	}
}

// RoundTrip buffers the request body, signs the clone, and delegates to
// the wrapped transport.
//
// FR-024 [HIGH]: http.Request.Clone is shallow on Body — clone.Body
// and req.Body share the same io.ReadCloser pointer. Reading clone.Body
// drained the caller's req.Body too, so outer retry/auth middleware
// that re-reads the original saw an empty body. The fix buffers the
// body once, then restores independent fresh readers on BOTH req and
// clone and sets GetBody on the clone so net/http's redirect /
// 100-Continue replay path works without consuming the caller's
// reader.
func (t *SigningTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	body, err := bufferBody(req, t.maxBodySize)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Body = io.NopCloser(bytes.NewReader(body))
		req.ContentLength = int64(len(body))
	}

	clone := req.Clone(req.Context())
	if body != nil {
		clone.Body = io.NopCloser(bytes.NewReader(body))
		clone.ContentLength = int64(len(body))
		clone.GetBody = func() (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(body)), nil
		}
	}

	if err := SignRequest(clone, body, t.store, t.opts...); err != nil {
		return nil, err
	}

	return t.base.RoundTrip(clone)
}

// bufferBody drains the request body into memory (up to max bytes) and
// closes the original reader. Returns (nil, nil) for bodyless requests.
// Returns an error if the body exceeds max — silently truncating would
// let the signed payload diverge from what the server eventually
// receives.
func bufferBody(req *http.Request, max int64) ([]byte, error) {
	if req.Body == nil || req.Body == http.NoBody {
		return nil, nil
	}
	buf, err := io.ReadAll(io.LimitReader(req.Body, max+1))
	closeErr := req.Body.Close()
	if err != nil {
		return nil, fmt.Errorf("reqsign: reading request body: %w", err)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("reqsign: closing request body: %w", closeErr)
	}
	if int64(len(buf)) > max {
		return nil, fmt.Errorf("reqsign: request body exceeds %d bytes", max)
	}
	return buf, nil
}
