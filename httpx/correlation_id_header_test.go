package httpx_test

import (
	"testing"

	"github.com/bds421/rho-kit/httpx"
	"github.com/bds421/rho-kit/httpx/middleware/correlationid"
)

// TestCorrelationIDHeaderMatchesMiddleware asserts that the private header
// constant in httpx stays in sync with the canonical correlationid.Header.
func TestCorrelationIDHeaderMatchesMiddleware(t *testing.T) {
	if httpx.CorrelationIDHeaderName != correlationid.Header {
		t.Errorf("header mismatch: httpx=%q, middleware=%q", httpx.CorrelationIDHeaderName, correlationid.Header)
	}
}
