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
package redact

import (
	"errors"
	"fmt"
	"log/slog"
)

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
	for {
		next := errors.Unwrap(unwrapped)
		if next == nil {
			break
		}
		unwrapped = next
	}
	return fmt.Sprintf("<redacted error: %T>", unwrapped)
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
	const maxFrames = 16
	out := make([]string, 0, 4)
	cur := err
	for i := 0; i < maxFrames && cur != nil; i++ {
		out = append(out, fmt.Sprintf("%T", cur))
		cur = errors.Unwrap(cur)
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
