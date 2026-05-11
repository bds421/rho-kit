package reqsign

import (
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"time"

	"github.com/bds421/rho-kit/crypto/v2/signing"
	"github.com/bds421/rho-kit/httpx/v2/middleware/signedrequest"
)

// freshNonceStoreOpt returns a VerifyOption that wires a brand-new
// in-memory nonce store. Tests use this so each subtest is isolated
// from replays in earlier subtests. 10 minutes is comfortably larger
// than the test fixtures' clock skew.
func freshNonceStoreOpt() VerifyOption {
	return WithNonceStore(signedrequest.NewMemoryNonceStore(10 * time.Minute))
}

// roundTripFunc adapts a function to http.RoundTripper.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

// testKey generates a deterministic byte sequence of the given size for testing.
// The seed parameter ensures distinct keys for different call sites.
// NOT suitable for production use — use crypto/rand for real keys.
func testKey(n int, seed int) []byte {
	k := make([]byte, n)
	for i := range k {
		k[i] = byte((i*7 + seed) % 256)
	}
	return k
}

func testNonce(seed string) string {
	sum := sha256.Sum256([]byte(seed))
	return base64.StdEncoding.EncodeToString(sum[:16])
}

// testStore returns a StaticKeyStore with two deterministic keys for testing.
func testStore() *signing.StaticKeyStore {
	return signing.NewStaticKeyStore(map[string][]byte{
		"primary":   testKey(32, 1),
		"secondary": testKey(48, 2),
	}, "primary")
}

// fixedClock returns a clock function that always returns t.
func fixedClock(t time.Time) func() time.Time {
	return func() time.Time { return t }
}
