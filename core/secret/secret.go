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
package secret

import (
	"fmt"
	"sync"
)

// redacted is the literal emitted by every formatting/marshalling path.
const redacted = "<redacted>"

// String wraps a sensitive value. The zero value is usable and represents
// an empty (already-zeroed) secret.
//
// String is safe for concurrent reads via [String.Reveal]/[String.RevealString].
// Concurrent [String.Close] races with reads as expected — callers should
// avoid revealing a string they are about to close.
type String struct {
	mu  sync.RWMutex
	buf []byte
}

// New takes ownership of the passed bytes by copying them into the
// String. The caller may zero or discard the input slice after the call
// returns.
//
// Passing nil yields an empty String.
func New(b []byte) *String {
	if len(b) == 0 {
		return &String{}
	}
	cp := make([]byte, len(b))
	copy(cp, b)
	return &String{buf: cp}
}

// NewFromString is the convenience form of [New] for string inputs.
func NewFromString(s string) *String {
	if s == "" {
		return &String{}
	}
	return &String{buf: []byte(s)}
}

// Reveal returns a copy of the underlying bytes. The returned slice is
// safe to mutate and does not share storage with the String. Callers
// should keep the lifetime of the returned slice short.
//
// Returns nil for a closed or empty String.
func (s *String) Reveal() []byte {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.buf) == 0 {
		return nil
	}
	cp := make([]byte, len(s.buf))
	copy(cp, s.buf)
	return cp
}

// RevealString returns the underlying value as a string.
//
// Returns "" for a closed or empty String.
func (s *String) RevealString() string {
	if s == nil {
		return ""
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return string(s.buf)
}

// IsEmpty reports whether the String holds no bytes (either it was
// constructed empty or [String.Close] zeroed it).
func (s *String) IsEmpty() bool {
	if s == nil {
		return true
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.buf) == 0
}

// Close zeroes the internal buffer. Subsequent [String.Reveal] calls
// return nil. Idempotent.
func (s *String) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.buf {
		s.buf[i] = 0
	}
	s.buf = nil
	return nil
}

// String implements fmt.Stringer.
func (s *String) String() string { return redacted }

// GoString implements fmt.GoStringer (used by %#v).
func (s *String) GoString() string { return redacted }

// MarshalJSON implements json.Marshaler.
func (s *String) MarshalJSON() ([]byte, error) {
	return []byte(`"` + redacted + `"`), nil
}

// MarshalText implements encoding.TextMarshaler. yaml.v3 also picks this up.
func (s *String) MarshalText() ([]byte, error) {
	return []byte(redacted), nil
}

// LogValue implements [log/slog.LogValuer] so structured logs that pass
// the String as a value (slog.Any) emit the redacted literal.
func (s *String) LogValue() any { return redacted }

// Format implements fmt.Formatter so all %v/%+v/%s/%q/%q variants emit
// the redacted literal.
func (s *String) Format(f fmt.State, verb rune) {
	switch verb {
	case 'q':
		_, _ = f.Write([]byte(`"` + redacted + `"`))
	default:
		_, _ = f.Write([]byte(redacted))
	}
}
