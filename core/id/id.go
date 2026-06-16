package id

import (
	"github.com/google/uuid"

	"github.com/bds421/rho-kit/core/v2/apperror"
)

// Generator is the package-level UUID v7 generator. The default is
// [New]; tests assign a deterministic closure here (and restore the
// previous value on cleanup) to keep log lines, fixtures, and golden
// files stable across runs. Concurrent calls must be safe for the
// installed function — [New] itself is, and so is any closure that
// only reads its captured state under a mutex.
//
// Generator is a plain, unsynchronized variable, so reassignment is a
// test-only affordance: install the swap during package or test setup,
// before any other goroutine reads it, and restore it on cleanup.
// Reassigning Generator while another goroutine calls it — including
// from a [testing.T.Parallel] test, or as a runtime swap in a live
// service — is a data race under the Go memory model. When concurrent
// code needs per-call determinism, thread a generator function through
// your own configuration rather than swapping this variable.
var Generator func() string = New

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
func New() string {
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
