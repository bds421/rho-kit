package contextutil

// MaxCorrelationIDLen is the shared maximum length for request and
// correlation identifiers accepted from transport metadata.
const MaxCorrelationIDLen = 128

// IsValidCorrelationToken reports whether id is a safe request/correlation
// identifier for propagation, response headers, and logs.
func IsValidCorrelationToken(id string, maxLen int) bool {
	if id == "" || maxLen <= 0 || len(id) > maxLen {
		return false
	}
	for i := 0; i < len(id); i++ {
		if !isCorrelationTokenByte(id[i]) {
			return false
		}
	}
	return true
}

func isCorrelationTokenByte(c byte) bool {
	return (c >= 'a' && c <= 'z') ||
		(c >= 'A' && c <= 'Z') ||
		(c >= '0' && c <= '9') ||
		c == '-' ||
		c == '_' ||
		c == '.'
}
