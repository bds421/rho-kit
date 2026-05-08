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
	"sync"
)

// redacted is the literal emitted by every formatting/marshalling path.
const redacted = "<redacted>"

// String wraps a sensitive value. The zero value is usable and
// represents an empty (already-zeroed) secret.
//
// String is safe for concurrent reads via [String.Reveal] / [String.RevealString].
// Concurrent [String.Close] races with reads as expected — callers should
// avoid revealing a string they are about to close.
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
// constructed without going through [New]), or a closed/empty String.
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
// closed/empty String.
func (s *String) RevealString() string {
	if s == nil || s.inner == nil {
		return ""
	}
	s.inner.mu.RLock()
	defer s.inner.mu.RUnlock()
	return string(s.inner.buf)
}

// IsEmpty reports whether the String holds no bytes (either it was
// constructed empty, was never initialised via [New], or [String.Close]
// zeroed it).
func (s *String) IsEmpty() bool {
	if s == nil || s.inner == nil {
		return true
	}
	s.inner.mu.RLock()
	defer s.inner.mu.RUnlock()
	return len(s.inner.buf) == 0
}

// Close zeroes the internal buffer. Subsequent [String.Reveal] calls
// return nil. Idempotent. No-op on a nil receiver or uninitialised
// String.
func (s *String) Close() error {
	if s == nil || s.inner == nil {
		return nil
	}
	s.inner.mu.Lock()
	defer s.inner.mu.Unlock()
	for i := range s.inner.buf {
		s.inner.buf[i] = 0
	}
	s.inner.buf = nil
	return nil
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
	return constantTimeEqual(a, b)
}

// constantTimeEqual compares two byte slices in constant time relative
// to max(len(a), len(b)). Returns true iff a and b have identical
// length and content.
func constantTimeEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	var v byte
	for i := 0; i < len(a); i++ {
		v |= a[i] ^ b[i]
	}
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
func (s String) LogValue() any { return redacted }

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
