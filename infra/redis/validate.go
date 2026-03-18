package redis

import (
	"fmt"
	"strings"
)

const (
	// maxNameLen is the maximum allowed length for stream, queue, and cache names.
	// Redis keys can technically be up to 512MB, but extremely long names waste
	// memory and indicate a likely programming error (e.g. embedding a request ID).
	maxNameLen = 256
)

// ValidateName checks that a resource name (stream, queue, cache) is safe for
// use as a Redis key and as a Prometheus metric label. This prevents:
//   - Null bytes: can truncate C strings in Redis internals
//   - Newlines/carriage returns: can break RESP protocol framing
//   - Empty names: always a programming error
//   - Excessively long names: waste memory and indicate dynamic data in keys
func ValidateName(name, kind string) error {
	if name == "" {
		return fmt.Errorf("%s name must not be empty", kind)
	}
	if len(name) > maxNameLen {
		return fmt.Errorf("%s name exceeds maximum length of %d bytes", kind, maxNameLen)
	}
	if strings.ContainsAny(name, "\x00\n\r") {
		return fmt.Errorf("%s name contains invalid characters (null byte, newline, or carriage return)", kind)
	}
	return nil
}
