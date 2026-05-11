package redis

import (
	"fmt"
	"unicode"
	"unicode/utf8"
)

const (
	// maxNameLen is the maximum allowed length for stream, queue, and cache names.
	// Redis keys can technically be up to 512MB, but extremely long names waste
	// memory and indicate a likely programming error (e.g. embedding a request ID).
	maxNameLen = 256
)

// ValidateName checks that a resource name (stream, queue, cache) is safe for
// use as a Redis key and as a Prometheus metric label. This prevents:
//   - Invalid UTF-8: corrupts logs and metric/debug output
//   - Whitespace/control characters: can break logs, CLI tooling, protocol
//     framing, or label readability
//   - Empty names: always a programming error
//   - Excessively long names: waste memory and indicate dynamic data in keys
func ValidateName(name, kind string) error {
	if name == "" {
		return fmt.Errorf("%s name must not be empty", kind)
	}
	if len(name) > maxNameLen {
		return fmt.Errorf("%s name exceeds maximum length of %d bytes", kind, maxNameLen)
	}
	if containsInvalidNameRune(name) {
		return fmt.Errorf("%s name contains invalid characters (whitespace or control characters)", kind)
	}
	return nil
}

func containsInvalidNameRune(name string) bool {
	if !utf8.ValidString(name) {
		return true
	}
	for _, r := range name {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return true
		}
	}
	return false
}
