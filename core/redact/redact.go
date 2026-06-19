// Package redact provides shared helpers for rendering sensitive values
// safely in logs and errors.
//
// The kit's logging convention is: never put attacker-controlled or
// tenant-controlled strings into a log record verbatim. This package
// replaces the value with a fixed-shape stamp (length, "redacted",
// empty marker) so operators can still distinguish "configured but
// empty" from "configured with content" without leaking topology,
// credentials, PII, or attacker-supplied content.
//
// Key entry points:
//
//   - [StringValue] / [String] — redact a string and return either the
//     scalar or a [log/slog.Attr].
//   - [ErrorValue] / [Error] — collapse an error into a sentinel-aware
//     summary; recognised kit sentinels keep their identity, everything
//     else loses its message.
//   - [Panic] / [PanicValue] — turn a recovered panic value into a safe
//     slog attribute and printable string.
//
// Compare with [crypto/masking], which keeps a structural prefix (the
// scheme/host of a URL, the first few runes of a token). Use redact
// when even structure is unsafe; use masking when partial visibility
// is debugging-useful.
//
// # Safe error wrapping across trust boundaries
//
// Standard `fmt.Errorf("prefix: %w", err)` preserves `errors.Is`/`As`
// chains but renders the inner error's text verbatim via `Error()`.
// When the wrapped error comes from an external driver or SDK its
// text may include tenant-controlled keys, internal hostnames, query
// fragments, or other content unsafe to surface verbatim into HTTP
// response bodies or untrusted log sinks.
//
// Use [WrapError] when wrapping a single backend cause and
// [WrapSentinel] when joining a kit sentinel with a backend cause.
// Both preserve unwrap chains so existing `errors.Is(err, sentinel)`
// call sites keep working, but render `.Error()` as
// `<prefix>: <redacted error: T>` rather than including the inner
// text.
package redact

import (
	"fmt"
	"log/slog"
)

// maxErrorFrames bounds how many frames the unwrap walkers descend
// through. Real error chains are 2–5 deep; the cap exists so a
// pathological wrap-loop (an error whose Unwrap returns an ancestor)
// cannot spin the walkers forever or exhaust memory.
const maxErrorFrames = 16

// StringValue returns a redacted representation of a runtime string.
//
// Runtime identifiers such as message IDs, queue names, storage paths, and
// backend endpoints often come from tenants, operators, or upstream systems.
// Keep only length information so logs can distinguish empty/missing values
// without copying topology, PII, or attacker-controlled text.
func StringValue(value string) string {
	if value == "" {
		return "<redacted empty>"
	}
	return fmt.Sprintf("<redacted %d bytes>", len(value))
}

// String returns a redacted slog attribute for a runtime string value.
func String(key, value string) slog.Attr {
	return slog.String(key, StringValue(value))
}

// ErrorValue returns a redacted representation of an error.
//
// Error strings from SDKs, brokers, storage backends, or user callbacks often
// include request URLs, object keys, message IDs, SQL fragments, or request
// payload data. Preserve the concrete error type for triage and errors.As
// reasoning in tests, but do not render Error().
func ErrorValue(err error) string {
	if err == nil {
		return "<nil>"
	}
	unwrapped := err
	// Bound the descent at maxErrorFrames so a pathological wrap-loop
	// (a buggy error whose Unwrap returns an ancestor) cannot spin this
	// loop forever — the same threat model ErrorChainTypes guards.
	for i := 0; i < maxErrorFrames; i++ {
		next := unwrapCause(unwrapped)
		if next == nil {
			break
		}
		unwrapped = next
	}
	return fmt.Sprintf("<redacted error: %T>", unwrapped)
}

// unwrapCause returns the cause to descend into for ErrorValue. It
// handles both single-error wrappers (Unwrap() error) and multi-error
// wrappers (Unwrap() []error, as produced by errors.Join, fmt.Errorf
// with multiple %w, and this package's own WrapSentinel). For the
// multi-error form it follows the last branch, which is the conventional
// "cause" position (fmt.Errorf("%w: %w", sentinel, cause) and
// sentinelWrappedError both place the deepest cause last), so the
// surfaced type is the underlying cause rather than the wrapper.
func unwrapCause(err error) error {
	switch x := err.(type) {
	case interface{ Unwrap() error }:
		return x.Unwrap()
	case interface{ Unwrap() []error }:
		causes := x.Unwrap()
		if len(causes) == 0 {
			return nil
		}
		return causes[len(causes)-1]
	default:
		return nil
	}
}

// Error returns the standard redacted slog attribute for an error.
func Error(err error) slog.Attr {
	return slog.String("error", ErrorValue(err))
}

// ErrorChainTypes returns the list of concrete Go types in err's
// errors.Unwrap chain, deepest cause last. The chain is bounded at 16
// frames so a pathological wrap-loop cannot exhaust memory; in practice
// real error chains are 2–5 deep.
//
// Type names are kit-controlled (or well-known SDK types) and never
// contain caller-supplied content, so this is safe to emit on
// fatal-startup / operator-facing log lines where a redacted message
// alone leaves operators without enough triage information to identify
// the failing subsystem.
func ErrorChainTypes(err error) []string {
	if err == nil {
		return nil
	}
	out := make([]string, 0, 4)
	// Bounded depth-first walk. Both unwrap forms are handled so
	// multi-error wrappers (errors.Join, fmt.Errorf with multiple %w,
	// WrapSentinel) contribute their branch types instead of stopping
	// the chain at the wrapper. maxErrorFrames caps the total number of
	// frames emitted so a pathological wrap-loop cannot exhaust memory.
	stack := []error{err}
	for len(stack) > 0 && len(out) < maxErrorFrames {
		cur := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if cur == nil {
			continue
		}
		out = append(out, fmt.Sprintf("%T", cur))
		switch x := cur.(type) {
		case interface{ Unwrap() error }:
			if next := x.Unwrap(); next != nil {
				stack = append(stack, next)
			}
		case interface{ Unwrap() []error }:
			causes := x.Unwrap()
			// Push in reverse so the first branch is visited first.
			for i := len(causes) - 1; i >= 0; i-- {
				if causes[i] != nil {
					stack = append(stack, causes[i])
				}
			}
		}
	}
	return out
}

// ErrorChain returns a slog.Attr listing the concrete Go types in
// err's errors.Unwrap chain. Pair with [Error] when the message must
// stay redacted but operators still need to know which subsystem
// failed (the canonical use case is the fatal-exit log from the
// service bootstrap).
func ErrorChain(err error) slog.Attr {
	return slog.Any("error_chain", ErrorChainTypes(err))
}

// ErrorKey returns a redacted slog attribute for an error under key.
func ErrorKey(key string, err error) slog.Attr {
	return slog.String(key, ErrorValue(err))
}

// PanicValue returns a redacted representation of a recovered panic value.
//
// Panic payloads often come from user callbacks or request handlers, and those
// payloads can contain tokens, credentials, request bodies, or full domain
// structs. Keep the concrete type for triage, but never include the value.
func PanicValue(v any) string {
	if v == nil {
		return "<redacted panic value: <nil>>"
	}
	return fmt.Sprintf("<redacted panic value: %T>", v)
}

// Panic returns the standard redacted slog attribute for a recovered panic.
func Panic(v any) slog.Attr {
	return slog.String("panic", PanicValue(v))
}

// WrapError returns an error that joins prefix with err while making
// Error() safe to render verbatim across trust boundaries.
//
// The standard fmt.Errorf("prefix: %w", err) pattern preserves
// errors.Is/As chains but Error() includes err.Error() — which, when
// err originates from an external driver or SDK, may contain
// tenant-controlled keys, internal hostnames, query fragments, or
// other content unsafe to surface in HTTP response bodies or
// untrusted logs.
//
// WrapError preserves the unwrap chain (errors.Is/As against the
// returned value still finds err and its ancestors) but renders
// Error() as `<prefix>: <redacted error: T>` where T is the deepest
// cause's concrete type. Returns nil when err is nil.
func WrapError(prefix string, err error) error {
	if err == nil {
		return nil
	}
	return &wrappedError{prefix: prefix, inner: err}
}

type wrappedError struct {
	prefix string
	inner  error
}

func (w *wrappedError) Error() string {
	return w.prefix + ": " + ErrorValue(w.inner)
}

func (w *wrappedError) Unwrap() error { return w.inner }

// WrapSentinel returns an error that joins sentinel with cause so
// errors.Is matches either, while Error() prints
// `<sentinel.Error()>: <redacted error: T>` rather than including
// cause's text.
//
// Use this when callers need to differentiate a known failure mode
// (the sentinel) and still preserve the underlying driver error for
// triage on the log path, but cause's Error() is unsafe to
// propagate verbatim. Equivalent to fmt.Errorf("%w: %w", sentinel,
// cause) for errors.Is/As purposes but with safe rendering.
//
// Returns nil when cause is nil. Panics if sentinel is nil — the
// caller's intent is to attach a known sentinel, so a nil sentinel
// is a programmer error rather than a runtime condition to absorb.
func WrapSentinel(sentinel, cause error) error {
	if sentinel == nil {
		panic("redact: WrapSentinel requires a non-nil sentinel")
	}
	if cause == nil {
		return nil
	}
	return &sentinelWrappedError{sentinel: sentinel, cause: cause}
}

type sentinelWrappedError struct {
	sentinel error
	cause    error
}

func (s *sentinelWrappedError) Error() string {
	return s.sentinel.Error() + ": " + ErrorValue(s.cause)
}

func (s *sentinelWrappedError) Unwrap() []error {
	return []error{s.sentinel, s.cause}
}
