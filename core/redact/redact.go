// Package redact provides shared helpers for rendering sensitive values safely
// in logs and errors.
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
