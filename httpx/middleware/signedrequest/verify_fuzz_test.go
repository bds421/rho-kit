package signedrequest

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// FuzzVerifyHeaders feeds arbitrary header values to verify() to prove
// the header parser handles every input as either a typed
// classification (missing/malformed/expired/clock-skew/bad-signature)
// or a successful verify — never a panic. The body is held empty so
// the fuzz focuses on header parsing rather than amplification.
func FuzzVerifyHeaders(f *testing.F) {
	resolver := func(_ context.Context, _ string) ([]byte, error) {
		return []byte(strings.Repeat("k", 32)), nil
	}
	cfg := &config{
		resolver:        resolver,
		nonceStore:      NewMemoryNonceStore(time.Minute),
		maxClockSkew:    time.Minute,
		bodyMaxSize:     1 << 16,
		inMemoryBodyMax: 1024,
		now:             func() time.Time { return time.Unix(1750000000, 0) },
	}

	// Seed the corpus with one well-formed request so the fuzz has a
	// known-good baseline and one obviously-broken signature.
	for _, sig := range []string{
		"",
		"hmac-sha256=AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=",
		"not-a-mac",
		strings.Repeat("A", 1<<10),
	} {
		for _, ts := range []string{"", "0", "1750000000", "notanumber", strings.Repeat("9", 1<<10)} {
			for _, nonce := range []string{"", "AAAAAAAAAAAAAAAAAAAAAA==", "invalid_nonce", strings.Repeat("z", 1<<10)} {
				f.Add(ts, nonce, "test-key", sig)
			}
		}
	}

	f.Fuzz(func(t *testing.T, ts, nonce, keyID, sig string) {
		req := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(""))
		if ts != "" {
			req.Header.Set(HeaderTimestamp, ts)
		}
		if nonce != "" {
			req.Header.Set(HeaderNonce, nonce)
		}
		if keyID != "" {
			req.Header.Set(HeaderKeyID, keyID)
		}
		if sig != "" {
			req.Header.Set(HeaderSignature, sig)
		}
		_ = verify(req, cfg) // must not panic
	})
}
