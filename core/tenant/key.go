package tenant

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// MaxKeyPartLen bounds each caller-supplied segment passed to [Key] or
// [KeyFor]. The cap is intentionally aligned with the cache key limit so
// tenant-scoped keys remain suitable for Redis, logs, and metric exemplars.
const MaxKeyPartLen = 1024

// MaxKeyParts caps the number of caller-supplied parts a single
// [Key]/[KeyFor] call accepts. Without it, a caller could append
// thousands of small parts and produce a multi-KB key that still
// passes the per-part cap.
const MaxKeyParts = 32

// MaxKeyTotalLen bounds the assembled length of a tenant-scoped key.
// Even with the per-part and part-count caps, the final composed key
// can grow large; this acts as the final defense so logs, metric
// labels, and Redis key buffers stay reasonable. Wave 68 added the
// total cap to close a hostile-review finding.
const MaxKeyTotalLen = 16 * 1024

// ErrKeyInvalid is returned when a tenant-scoped key cannot be built because
// one of its caller-supplied parts is empty, too long, invalid UTF-8, or
// contains whitespace/control bytes that corrupt logs or backend protocol
// framing.
var ErrKeyInvalid = errors.New("tenant: key part is invalid")

// Key returns the kit-canonical tenant-scoped key for ctx and parts.
//
// The output format is length-prefixed:
//
//	tenant:<len(id)>:<id>:<len(part)>:<part>...
//
// Length-prefixing every variable field prevents collisions such as
// tenant "a:b" + part "c" and tenant "a" + part "b:c". Use this helper
// instead of hand-written fmt.Sprintf prefixes for tenant-scoped Redis,
// cache, idempotency, budget, and storage-adjacent keys.
func Key(ctx context.Context, parts ...string) (string, error) {
	id, err := Required(ctx)
	if err != nil {
		return "", err
	}
	return KeyFor(id, parts...)
}

// KeyFor is like [Key], but takes an already-resolved tenant ID. It is useful
// in code that already validated or loaded the tenant at a request boundary.
func KeyFor(id ID, parts ...string) (string, error) {
	if id.IsZero() {
		return "", ErrMissing
	}
	if err := validateKeyPart(string(id)); err != nil {
		return "", fmt.Errorf("%w for tenant ID: %w", ErrKeyInvalid, err)
	}
	if len(parts) == 0 {
		return "", fmt.Errorf("%w: at least one part is required", ErrKeyInvalid)
	}
	if len(parts) > MaxKeyParts {
		return "", fmt.Errorf("%w: exceeds maximum part count %d", ErrKeyInvalid, MaxKeyParts)
	}
	var b strings.Builder
	b.WriteString("tenant")
	writeKeyField(&b, string(id))
	for i, part := range parts {
		if err := validateKeyPart(part); err != nil {
			return "", fmt.Errorf("%w at index %d: %w", ErrKeyInvalid, i, err)
		}
		writeKeyField(&b, part)
	}
	if b.Len() > MaxKeyTotalLen {
		return "", fmt.Errorf("%w: assembled key exceeds maximum total length %d", ErrKeyInvalid, MaxKeyTotalLen)
	}
	return b.String(), nil
}

func writeKeyField(b *strings.Builder, value string) {
	if b.Len() > 0 {
		b.WriteByte(':')
	}
	b.WriteString(strconv.Itoa(len(value)))
	b.WriteByte(':')
	b.WriteString(value)
}

func validateKeyPart(part string) error {
	if part == "" {
		return errors.New("must not be empty")
	}
	if len(part) > MaxKeyPartLen {
		return errors.New("exceeds maximum length")
	}
	if containsInvalidKeyRune(part) {
		return errors.New("contains invalid or forbidden bytes")
	}
	return nil
}

func containsInvalidKeyRune(s string) bool {
	if !utf8.ValidString(s) {
		return true
	}
	for _, r := range s {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return true
		}
	}
	return false
}
