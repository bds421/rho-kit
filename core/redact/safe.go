package redact

import (
	"fmt"
	"log/slog"
	"strings"
)

// SafeMessager is implemented by errors whose message is safe to log
// verbatim. The message must be built only from constant text and
// non-secret values (environment variable names, option names, limits),
// never from backend error text, URLs, credentials, paths, or tenant
// content.
//
// [SafeMessages] collects these messages from an error chain so a
// fatal-startup log can say what went wrong while every frame that is not
// marked safe stays redacted.
type SafeMessager interface {
	SafeMessage() string
}

// Safe returns an error whose message is safe to log. Use it in place of
// errors.New for configuration and programming errors, such as a missing
// required environment variable or a builder gate.
func Safe(msg string) error {
	return &safeError{msg: msg}
}

// Safef is [Safe] with fmt.Sprintf formatting. The caller asserts every
// argument is non-secret; it cannot wrap an error (%w), so a cause's text
// can never become part of the safe message. Use [WrapSafe] to add a safe
// message in front of a cause.
func Safef(format string, args ...any) error {
	return &safeError{msg: fmt.Sprintf(format, args...)}
}

type safeError struct {
	msg string
}

func (e *safeError) Error() string       { return e.msg }
func (e *safeError) SafeMessage() string { return e.msg }

// WrapSafe returns an error that prefixes err with msg like
// fmt.Errorf("%s: %w", msg, err): Error() includes err's text and the
// unwrap chain is kept, but only msg is reported as safe. The cause
// contributes to [SafeMessages] only through its own safe frames. Use
// [WrapError] instead when Error() itself crosses a trust boundary.
// Returns nil when err is nil.
func WrapSafe(msg string, err error) error {
	if err == nil {
		return nil
	}
	return &safeWrappedError{msg: msg, inner: err}
}

type safeWrappedError struct {
	msg   string
	inner error
}

func (e *safeWrappedError) Error() string       { return e.msg + ": " + e.inner.Error() }
func (e *safeWrappedError) SafeMessage() string { return e.msg }
func (e *safeWrappedError) Unwrap() error       { return e.inner }

// SafeMessages returns the safe messages found in err's unwrap chain,
// outermost first and joined by ": ", or "" when no frame is marked safe.
// Frames that do not implement [SafeMessager] contribute nothing. The walk
// covers both unwrap forms and is bounded like [ErrorChainTypes].
func SafeMessages(err error) string {
	var parts []string
	walkChain(err, func(cur error) {
		if s, ok := cur.(SafeMessager); ok {
			if msg := s.SafeMessage(); msg != "" {
				parts = append(parts, msg)
			}
		}
	})
	return strings.Join(parts, ": ")
}

// ErrorMessage returns an "error_message" slog attribute with
// [SafeMessages], or an empty attribute (which slog drops) when the chain
// holds no safe message. Pair it with [Error] and [ErrorChain] on
// operator-facing failure logs.
func ErrorMessage(err error) slog.Attr {
	msg := SafeMessages(err)
	if msg == "" {
		return slog.Attr{}
	}
	return slog.String("error_message", msg)
}
