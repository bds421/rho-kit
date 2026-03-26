package reqsign

import (
	"net/http"
	"time"

	"github.com/bds421/rho-kit/crypto/signing"
)

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
