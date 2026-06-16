// Package logattr provides standard slog.Attr constructors for consistent
// structured logging across the kit. Using these constructors ensures field
// names are uniform, queryable, and typo-free.
package logattr

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"github.com/bds421/rho-kit/core/v2/redact"
)

// Error returns a redacted "error" attribute. Prefer this over slog.String("error", ...).
func Error(err error) slog.Attr {
	return redact.Error(err)
}

// Component returns a "component" attribute identifying a lifecycle component.
func Component(name string) slog.Attr {
	return slog.String("component", name)
}

// RequestID returns a "request_id" attribute for correlating log lines.
func RequestID(id string) slog.Attr {
	return slog.String("request_id", id)
}

// Addr returns a redacted "addr" attribute (host:port).
func Addr(addr string) slog.Attr {
	return redact.String("addr", addr)
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

// Path returns a redacted "path" attribute for HTTP paths.
func Path(p string) slog.Attr {
	return redact.String("path", p)
}

// StatusCode returns a "status" attribute for HTTP status codes.
func StatusCode(code int) slog.Attr {
	return slog.Int("status", code)
}

// Instance returns a redacted "instance" attribute for named instances (DB, cache, etc.).
func Instance(name string) slog.Attr {
	return redact.String("instance", name)
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

// UserID returns a redacted "user_id" attribute.
func UserID(id string) slog.Attr {
	return redact.String("user_id", id)
}

// Count returns a "count" attribute for batch operations.
func Count(n int) slog.Attr {
	return slog.Int("count", n)
}

// Operation returns a redacted "operation" attribute for audit/action logging.
func Operation(name string) slog.Attr {
	return redact.String("operation", name)
}

// Queue returns a redacted "queue" attribute for message queue logging.
func Queue(name string) slog.Attr {
	return redact.String("queue", name)
}

// Topic returns a redacted "topic" attribute for message bus logging.
func Topic(name string) slog.Attr {
	return redact.String("topic", name)
}

// Stream returns a redacted "stream" attribute for event stream logging.
func Stream(name string) slog.Attr {
	return redact.String("stream", name)
}

// URL returns a redacted "url" attribute for HTTP client logging.
func URL(raw string) slog.Attr {
	return slog.String("url", redactedURL(raw))
}

// Secret returns a redaction-safe attribute for sensitive values
// (Authorization headers, JWTs, API keys, password fields, etc.).
//
// FR-085 [LOW]: by default the rendering is "<redacted N bytes>" —
// no digest. Pre-fix the value carried the first 8 hex chars of
// SHA-256, which is brute-forceable for low-entropy secrets like
// 6-digit OTPs or short reset codes. Operators that need
// across-line correlation (and accept the brute-force cost) can use
// [SecretWithDigest], which still emits a SHA-256 prefix.
//
// An empty value emits "<redacted empty>".
//
// Use Secret instead of slog.String for any field that must never appear
// verbatim in logs — slog.String has no awareness of secrecy.
func Secret(key, value string) slog.Attr {
	return slog.String(key, redactedValueNoDigest(value))
}

// SecretWithDigest is the legacy correlation-friendly redaction.
// The first 8 hex chars of SHA-256 leak ~32 bits — fine for a
// 32-byte JWT, brute-forceable for a 6-digit OTP. Use only for
// high-entropy secrets.
func SecretWithDigest(key, value string) slog.Attr {
	return slog.String(key, redactedValue(value))
}

func redactedValueNoDigest(value string) string {
	if value == "" {
		return "<redacted empty>"
	}
	return fmt.Sprintf("<redacted %d bytes>", len(value))
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

// redactedURL fully redacts raw down to a length-only marker; the URL is
// never rendered verbatim regardless of validity.
//
// The url.Parse check does NOT perform meaningful validation: url.Parse is
// extremely lenient and accepts almost any string (e.g. "not a url" or
// "javascript:alert(1)") without error. It only rejects gross syntactic
// malformations such as a missing scheme ("://invalid"), a bad port, or
// control characters. Those rare cases render the literal "[INVALID URL]"
// marker instead of the length marker; every other input — valid or not —
// falls through to the identical redacted form. The distinction is purely a
// cosmetic hint for log readers and carries no security weight, since the
// raw value is redacted on both paths.
func redactedURL(raw string) string {
	if raw == "" {
		return redact.StringValue(raw)
	}
	if _, err := url.Parse(raw); err != nil {
		return "[INVALID URL]"
	}
	return redact.StringValue(raw)
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
