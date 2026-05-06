// Package logattr provides standard slog.Attr constructors for consistent
// structured logging across the kit. Using these constructors ensures field
// names are uniform, queryable, and typo-free.
package logattr

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// Error returns an "error" attribute. Prefer this over slog.String("error", ...).
func Error(err error) slog.Attr {
	return slog.Any("error", err)
}

// Component returns a "component" attribute identifying a lifecycle component.
func Component(name string) slog.Attr {
	return slog.String("component", name)
}

// RequestID returns a "request_id" attribute for correlating log lines.
func RequestID(id string) slog.Attr {
	return slog.String("request_id", id)
}

// Addr returns an "addr" attribute (host:port).
func Addr(addr string) slog.Attr {
	return slog.String("addr", addr)
}

// Attempt returns an "attempt" attribute for retry logging.
func Attempt(n int) slog.Attr {
	return slog.Int("attempt", n)
}

// Delay returns a "delay" attribute for backoff/wait durations.
func Delay(d time.Duration) slog.Attr {
	return slog.Duration("delay", d)
}

// Method returns a "method" attribute for HTTP methods.
func Method(m string) slog.Attr {
	return slog.String("method", m)
}

// Path returns a "path" attribute for HTTP paths.
func Path(p string) slog.Attr {
	return slog.String("path", p)
}

// StatusCode returns a "status" attribute for HTTP status codes.
func StatusCode(code int) slog.Attr {
	return slog.Int("status", code)
}

// Instance returns an "instance" attribute for named instances (DB, cache, etc.).
func Instance(name string) slog.Attr {
	return slog.String("instance", name)
}

// Duration returns a "duration" attribute for request/operation durations.
func Duration(d time.Duration) slog.Attr {
	return slog.Duration("duration", d)
}

// TraceID returns a "trace_id" attribute for distributed tracing correlation.
func TraceID(id string) slog.Attr {
	return slog.String("trace_id", id)
}

// SpanID returns a "span_id" attribute for distributed tracing correlation.
func SpanID(id string) slog.Attr {
	return slog.String("span_id", id)
}

// UserID returns a "user_id" attribute.
func UserID(id string) slog.Attr {
	return slog.String("user_id", id)
}

// Count returns a "count" attribute for batch operations.
func Count(n int) slog.Attr {
	return slog.Int("count", n)
}

// Operation returns an "operation" attribute for audit/action logging.
func Operation(name string) slog.Attr {
	return slog.String("operation", name)
}

// Queue returns a "queue" attribute for message queue logging.
func Queue(name string) slog.Attr {
	return slog.String("queue", name)
}

// Topic returns a "topic" attribute for message bus logging.
func Topic(name string) slog.Attr {
	return slog.String("topic", name)
}

// Stream returns a "stream" attribute for event stream logging.
func Stream(name string) slog.Attr {
	return slog.String("stream", name)
}

// URL returns a "url" attribute for HTTP client logging.
func URL(u string) slog.Attr {
	return slog.String("url", u)
}

// Secret returns a redaction-safe attribute for sensitive values
// (Authorization headers, JWTs, API keys, password fields, etc.).
//
// The value is replaced by "<redacted N bytes sha256:abc12345>", where:
//   - N is the original byte length (so log volume changes are still
//     visible);
//   - sha256:... is the first 8 hex chars of the SHA-256 digest, allowing
//     correlation across log lines without revealing the value.
//
// An empty value emits "<redacted empty>".
//
// Use Secret instead of slog.String for any field that must never appear
// verbatim in logs — slog.String has no awareness of secrecy.
func Secret(key, value string) slog.Attr {
	return slog.String(key, redactedValue(value))
}

// Email returns a redacted "email" attribute that masks the local part
// while keeping the domain visible for triage.
//
// Examples:
//
//	Email("alice@example.com")  -> email="a***@example.com"
//	Email("a@example.com")      -> email="*@example.com"
//	Email("malformed")          -> email="<redacted>"
//	Email("")                   -> email="<redacted empty>"
//
// Domain is preserved because operators commonly need to know whether a
// production incident hit gmail vs. a corporate domain. The local part
// is the identifying portion and is masked.
func Email(addr string) slog.Attr {
	return slog.String("email", maskEmail(addr))
}

func redactedValue(value string) string {
	if value == "" {
		return "<redacted empty>"
	}
	sum := sha256.Sum256([]byte(value))
	digest := hex.EncodeToString(sum[:])[:8]
	return fmt.Sprintf("<redacted %d bytes sha256:%s>", len(value), digest)
}

func maskEmail(addr string) string {
	if addr == "" {
		return "<redacted empty>"
	}
	at := strings.LastIndex(addr, "@")
	if at <= 0 || at == len(addr)-1 {
		return "<redacted>"
	}
	local := addr[:at]
	domain := addr[at:]
	if len(local) == 1 {
		return "*" + domain
	}
	return string(local[0]) + "***" + domain
}
