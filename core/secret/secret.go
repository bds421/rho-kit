// Package secret provides a [String] type that wraps sensitive values and
// refuses to render through the standard formatting and marshalling paths.
//
// The standard library makes accidental disclosure trivial:
//
//	cfg := struct{ Token string }{Token: "abc"}
//	slog.Info("config", "cfg", cfg)            // logs Token="abc"
//	json.Marshal(cfg)                          // serialises Token="abc"
//	fmt.Sprintf("%+v", cfg)                    // prints Token:abc
//
// All three paths trip over a [String] field — they emit "<redacted>"
// instead. To access the underlying value, callers must call
// [String.Reveal] or [String.RevealString] explicitly. That is the
// single place a code review can grep for to audit sensitive value reads.
//
// # Value- vs pointer-typed usage
//
// Both `var s secret.String` (value) and `secret.New(...)` (pointer) are
// safe. The redaction methods are defined on value receivers, so they
// remain in the method set after a deref / by-value embedding / struct
// copy — none of those usages can leak plaintext through the standard
// formatting paths. State (the underlying buffer + mutex) lives behind a
// pointer field on String, so by-value copies share the same backing
// state without copying the mutex (no `go vet "passes lock by value"`
// warning, no torn reads).
package secret

import (
	"fmt"
	"log/slog"
	"sync"
)

// redacted is the literal emitted by every formatting/marshalling path.
const redacted = "<redacted>"

// String wraps a sensitive value. The zero value is usable and
// represents an empty (already-zeroed) secret.
//
// String is safe for concurrent reads via [String.Reveal] / [String.RevealString].
// Concurrent [String.Zero] races with reads as expected — callers should
// avoid revealing a string they are about to zero.
//
// String is intentionally a thin wrapper around a pointer to internal
// state. By-value copies of String share the same backing state with the
// original, which is the only way to keep the redaction methods on value
// receivers (required for fmt/json/slog interfaces to dispatch through
// the redacted path even when the type is used by value) without falling
// foul of `go vet`'s mutex-copy lint.
type String struct {
	inner *stringInner
}

type stringInner struct {
	mu  sync.RWMutex
	buf []byte
}

// New takes ownership of the passed bytes by copying them into the
// String. The caller may zero or discard the input slice after the call
// returns.
//
// Passing nil yields an empty String.
func New(b []byte) *String {
	s := &String{inner: &stringInner{}}
	if len(b) == 0 {
		return s
	}
	cp := make([]byte, len(b))
	copy(cp, b)
	s.inner.buf = cp
	return s
}

// NewFromString is the convenience form of [New] for string inputs.
func NewFromString(str string) *String {
	s := &String{inner: &stringInner{}}
	if str == "" {
		return s
	}
	s.inner.buf = []byte(str)
	return s
}

// Reveal returns a copy of the underlying bytes. The returned slice is
// safe to mutate and does not share storage with the String. Callers
// should keep the lifetime of the returned slice short.
//
// Returns nil for a nil receiver, an uninitialised String (zero value
// constructed without going through [New]), or a zeroed/empty String.
func (s *String) Reveal() []byte {
	if s == nil || s.inner == nil {
		return nil
	}
	s.inner.mu.RLock()
	defer s.inner.mu.RUnlock()
	if len(s.inner.buf) == 0 {
		return nil
	}
	cp := make([]byte, len(s.inner.buf))
	copy(cp, s.inner.buf)
	return cp
}

// RevealString returns the underlying value as a string.
//
// Returns "" for a nil receiver, an uninitialised String, or a
// zeroed/empty String.
func (s *String) RevealString() string {
	if s == nil || s.inner == nil {
		return ""
	}
	s.inner.mu.RLock()
	defer s.inner.mu.RUnlock()
	return string(s.inner.buf)
}

// IsEmpty reports whether the String holds no bytes (either it was
// constructed empty, was never initialised via [New], or [String.Zero]
// zeroed it).
func (s *String) IsEmpty() bool {
	if s == nil || s.inner == nil {
		return true
	}
	s.inner.mu.RLock()
	defer s.inner.mu.RUnlock()
	return len(s.inner.buf) == 0
}

// Use grants temporary access to a copy of the underlying bytes
// scoped to the lifetime of fn. The slice passed to fn is fresh
// storage that does NOT alias the internal buffer; it is overwritten
// with zeroes after fn returns (even when fn panics), so callers must
// not retain the slice or any sub-slice beyond fn's return.
//
// Compared to [String.Reveal], Use bounds the lifetime of the
// plaintext copy in heap memory — the temporary slice exists only
// for the call site that needs it and is wiped immediately
// afterwards. Use this for hot paths that compute an HMAC, AEAD
// key, or other cryptographic operation against the secret and have
// no reason to keep plaintext around afterwards.
//
// No-op on a nil receiver or an empty / zeroed String; fn is called
// with a nil slice in that case so callers can treat the closure
// uniformly.
func (s *String) Use(fn func(b []byte)) {
	if fn == nil {
		return
	}
	if s == nil || s.inner == nil {
		fn(nil)
		return
	}
	s.inner.mu.RLock()
	var local []byte
	if len(s.inner.buf) > 0 {
		local = make([]byte, len(s.inner.buf))
		copy(local, s.inner.buf)
	}
	s.inner.mu.RUnlock()
	defer func() {
		for i := range local {
			local[i] = 0
		}
	}()
	fn(local)
}

// Zero overwrites the internal buffer with zeroes. Subsequent
// [String.Reveal] calls return nil. Idempotent. No-op on a nil
// receiver or uninitialised String.
//
// Zero replaced the v1-era Close method: a [String] is not a
// resource that closes — Zero clears the underlying buffer. The
// renamed method also drops the (always-nil) error return so call
// sites stop treating it as an io.Closer.
func (s *String) Zero() {
	if s == nil || s.inner == nil {
		return
	}
	s.inner.mu.Lock()
	defer s.inner.mu.Unlock()
	for i := range s.inner.buf {
		s.inner.buf[i] = 0
	}
	s.inner.buf = nil
}

// Equal reports whether s and other carry equal byte sequences. The
// comparison runs in constant time relative to the secret length, so
// using Equal does not create a timing side-channel that distinguishes
// "right secret, wrong length" from "wrong secret, right length".
//
// nil and uninitialised Strings are treated as empty; two empty
// secrets compare equal.
func (s *String) Equal(other *String) bool {
	a, b := []byte(nil), []byte(nil)
	if s != nil && s.inner != nil {
		s.inner.mu.RLock()
		a = append(a, s.inner.buf...)
		s.inner.mu.RUnlock()
	}
	if other != nil && other.inner != nil {
		other.inner.mu.RLock()
		b = append(b, other.inner.buf...)
		other.inner.mu.RUnlock()
	}
	// Wipe the temporary plaintext copies before returning, mirroring
	// Use's lifetime-bounding semantics: Equal must not leave unzeroed
	// plaintext in GC-managed heap memory after the comparison.
	defer func() {
		for i := range a {
			a[i] = 0
		}
		for i := range b {
			b[i] = 0
		}
	}()
	return constantTimeEqual(a, b)
}

// constantTimeEqual compares two byte slices in constant time relative
// to max(len(a), len(b)). Returns true iff a and b have identical
// length and content.
//
// FR-041 [LOW]: pre-fix this returned early on length mismatch,
// observable by timing. The current implementation runs the XOR
// loop over max(len(a), len(b)), substituting zero bytes for the
// shorter side, and folds the length comparison into the result so
// "right secret, wrong length" cannot be distinguished from "wrong
// secret, right length" via timing.
//
// The length delta is folded as a uint (not byte) so length
// differences that are multiples of 256 cannot collapse to zero.
// A wave-66 review caught that the earlier byte cast meant a
// 256-byte longer all-zero suffix could falsely report equality.
func constantTimeEqual(a, b []byte) bool {
	maxLen := len(a)
	if len(b) > maxLen {
		maxLen = len(b)
	}
	var v uint
	for i := 0; i < maxLen; i++ {
		var ai, bi byte
		if i < len(a) {
			ai = a[i]
		}
		if i < len(b) {
			bi = b[i]
		}
		v |= uint(ai ^ bi)
	}
	v |= uint(len(a)) ^ uint(len(b))
	return v == 0
}

// The redaction methods use VALUE receivers so they remain in the method
// set when the type is used by value. Pointer (*String) automatically
// satisfies the same interfaces because Go promotes value-receiver
// methods into the pointer method set. The reverse is NOT true — if
// these were pointer-receiver-only, `var s String` (value) and any
// by-value embedding / copy / deref would not satisfy fmt.Stringer /
// json.Marshaler / encoding.TextMarshaler / slog.LogValuer /
// fmt.Formatter, and the standard formatting paths would print the
// underlying buf as a decimal byte slice — i.e. the plaintext, decoded.
//
// These methods do not read s.inner and do not need a mutex; they always
// emit the redacted literal regardless of state.

// String implements fmt.Stringer.
func (s String) String() string { return redacted }

// GoString implements fmt.GoStringer (used by %#v).
func (s String) GoString() string { return redacted }

// MarshalJSON implements json.Marshaler.
func (s String) MarshalJSON() ([]byte, error) {
	return []byte(`"` + redacted + `"`), nil
}

// MarshalText implements encoding.TextMarshaler. yaml.v3 also picks this up.
func (s String) MarshalText() ([]byte, error) {
	return []byte(redacted), nil
}

// LogValue implements [log/slog.LogValuer] so structured logs that pass
// the String as a value (slog.Any) emit the redacted literal.
//
// FR-042 [LOW]: returns slog.Value (not any) so the slog SDK
// recognises this as the LogValuer contract and recurses into it
// when formatting. Pre-fix the method returned `any`, which compiled
// but failed slog's type assertion — the redaction worked only
// because the other String formatters (Format/MarshalJSON) covered
// the typical print paths.
func (s String) LogValue() slog.Value { return slog.StringValue(redacted) }

// Compile-time assertion that String satisfies slog.LogValuer.
var _ slog.LogValuer = String{}

// Format implements fmt.Formatter so all %v/%+v/%s/%q/%q variants emit
// the redacted literal.
func (s String) Format(f fmt.State, verb rune) {
	switch verb {
	case 'q':
		_, _ = f.Write([]byte(`"` + redacted + `"`))
	default:
		_, _ = f.Write([]byte(redacted))
	}
}
