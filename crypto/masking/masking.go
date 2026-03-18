package masking

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/bds421/rho-kit/crypto/encrypt"
)

// DecryptAndMaskURL decrypts the URL (if encryptor is non-nil) then masks it,
// showing only scheme and host (e.g., "https://example.com/***").
// Returns "***" if the URL cannot be parsed.
func DecryptAndMaskURL(rawURL string, encryptor *encrypt.FieldEncryptor) string {
	if encryptor != nil {
		decrypted, err := encryptor.Decrypt(rawURL)
		if err == nil {
			rawURL = decrypted
		}
	}
	return MaskURL(rawURL)
}

// MaskURL returns a masked URL showing only scheme and host (e.g., "https://example.com/***").
// Returns "***" if the URL cannot be parsed or lacks a scheme/host (e.g., relative paths,
// mailto: URIs, or malformed input).
func MaskURL(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "***"
	}
	return fmt.Sprintf("%s://%s/***", parsed.Scheme, parsed.Host)
}

// MaskString returns the first n runes of s followed by "****".
// Returns "[REDACTED]" if s has fewer than or equal to n runes, preventing
// accidental exposure of short secrets.
// Operates on runes (not bytes) to avoid splitting multi-byte UTF-8 characters.
func MaskString(s string, n int) string {
	if n < 0 {
		n = 0
	}
	runes := []rune(s)
	if len(runes) <= n {
		return "[REDACTED]"
	}
	return string(runes[:n]) + strings.Repeat("*", 4)
}

// MaskMapValues returns a copy of the map with all values replaced by "***".
// Returns an empty map for nil or empty inputs (never returns nil).
func MaskMapValues(m map[string]string) map[string]string {
	if len(m) == 0 {
		return map[string]string{}
	}
	masked := make(map[string]string, len(m))
	for k := range m {
		masked[k] = "***"
	}
	return masked
}
