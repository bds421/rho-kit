package id

import (
	"sync/atomic"

	"github.com/google/uuid"

	"github.com/bds421/rho-kit/core/v2/apperror"
)

// generator holds the package-level ID source. Production code uses the
// default [New]; tests install a deterministic function via
// [SetGeneratorForTesting]. Reads are atomic so concurrent Generator
// calls are race-free; install/restore still belongs in serial test
// setup (before parallel work begins) so fixtures stay deterministic.
var generator atomic.Pointer[func() string]

func init() {
	restoreDefaultGenerator()
}

func restoreDefaultGenerator() {
	f := defaultNew
	generator.Store(&f)
}

// Generator returns a fresh ID from the currently installed package
// generator (default: [New]). Prefer calling [New] directly in
// production; use Generator only when code under test must honour a
// [SetGeneratorForTesting] swap.
func Generator() string {
	f := generator.Load()
	if f == nil || *f == nil {
		return defaultNew()
	}
	return (*f)()
}

// SetGeneratorForTesting installs g as the package-level ID source used
// by [Generator]. Pass nil to restore the default ([New]). Intended for
// tests that need stable IDs in log lines, fixtures, or golden files —
// call during package/test setup and restore with nil (or via
// t.Cleanup) before any parallel sibling may observe the swap.
//
// The installed function must be safe for concurrent calls (only reading
// captured state under its own synchronization). SetGeneratorForTesting
// itself is safe concurrent with Generator reads; it is not a production
// runtime reconfiguration API.
func SetGeneratorForTesting(g func() string) {
	if g == nil {
		restoreDefaultGenerator()
		return
	}
	// Store a pointer to a heap-copied func value so the caller's local
	// cannot be mutated out from under concurrent readers.
	fn := g
	generator.Store(&fn)
}

// New returns a fresh UUID v7 as a canonical 36-character string.
//
// UUID v7 is time-ordered (RFC 9562 §5.7): the leading 48 bits are a
// millisecond Unix timestamp, the trailing bits are random. That
// ordering plays well with B-tree indexes and with humans scanning
// logs chronologically, which is why the kit prefers it over v4 for
// internally-minted message and entry IDs.
//
// New panics on the (effectively impossible) [crypto/rand] failure
// case. Once the OS RNG is gone, a service cannot reliably mint
// session tokens, sign payloads, or seed retries; surfacing the
// failure as a returned error only kicks the panic down the call
// stack. Pre-v2 call sites that wrapped uuid.NewV7 in
// `if err != nil { return fmt.Errorf("generate ID: %w", err) }`
// should drop the branch when migrating.
//
// New always uses the real UUID v7 source. Test determinism goes through
// [Generator] + [SetGeneratorForTesting], not through New, so production
// call sites that hard-code New keep cryptographic freshness even if a
// test swap is active in-process.
func New() string {
	return defaultNew()
}

func defaultNew() string {
	u, err := uuid.NewV7()
	if err != nil {
		panic("id: New crypto/rand entropy exhausted")
	}
	return u.String()
}

// NewBytes is [New] returning the raw 16-byte form. Use it when the
// caller needs a uuid-typed value (Postgres uuid columns, binary wire
// encodings) and would otherwise immediately round-trip through
// [Parse] on a [New] string.
func NewBytes() [16]byte {
	u, err := uuid.NewV7()
	if err != nil {
		panic("id: NewBytes crypto/rand entropy exhausted")
	}
	return u
}

// Parse turns a canonical UUID string back into its 16-byte form. It
// accepts the same set of inputs as [github.com/google/uuid.Parse]
// (with or without dashes, with or without `urn:uuid:` prefix, mixed
// case). Invalid input is returned as a kit validation error so
// transport adapters can map it to a 4xx without further wrapping.
func Parse(s string) ([16]byte, error) {
	u, err := uuid.Parse(s)
	if err != nil {
		return [16]byte{}, apperror.NewValidationWithCause("id: Parse requires a valid UUID", err)
	}
	return u, nil
}
