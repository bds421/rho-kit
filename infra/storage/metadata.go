package storage

import (
	"fmt"
	"mime"
	"strings"
	"unicode/utf8"
)

const (
	maxContentTypeLen       = 255
	maxCustomMetaEntries    = 64
	maxCustomMetaKeyLen     = 128
	maxCustomMetaValueLen   = 1024
	maxCustomMetaTotalBytes = 2048
)

// CloneCustomMeta returns a shallow copy of custom object metadata.
// It preserves nil versus non-nil empty maps so callers can distinguish
// omitted metadata from an explicitly empty metadata map when needed.
func CloneCustomMeta(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// CloneObjectMeta returns a value copy of meta with a detached Custom map.
func CloneObjectMeta(meta ObjectMeta) ObjectMeta {
	meta.Custom = CloneCustomMeta(meta.Custom)
	return meta
}

// ValidateObjectMeta validates metadata before it is sent to a backend.
// Custom metadata is stored as provider-specific HTTP metadata headers, so
// this enforces a portable, bounded ASCII subset instead of trusting SDKs to
// reject malformed names or header-breaking values consistently.
func ValidateObjectMeta(meta ObjectMeta) error {
	if meta.Size < 0 {
		return fmt.Errorf("%w: object metadata size must not be negative", ErrValidation)
	}
	if err := validateContentType(meta.ContentType); err != nil {
		return err
	}
	if len(meta.Custom) > maxCustomMetaEntries {
		return fmt.Errorf("%w: custom metadata has too many entries", ErrValidation)
	}

	total := 0
	for k, v := range meta.Custom {
		if err := validateCustomMetaKey(k); err != nil {
			return fmt.Errorf("%w: custom metadata key is invalid", ErrValidation)
		}
		if len(v) > maxCustomMetaValueLen {
			return fmt.Errorf("%w: custom metadata value exceeds maximum length", ErrValidation)
		}
		if !isPrintableASCII(v) {
			return fmt.Errorf("%w: custom metadata value must contain printable ASCII only", ErrValidation)
		}
		total += len(k) + len(v)
		if total > maxCustomMetaTotalBytes {
			return fmt.Errorf("%w: custom metadata exceeds maximum total size", ErrValidation)
		}
	}
	return nil
}

func validateContentType(contentType string) error {
	if contentType == "" {
		return nil
	}
	if len(contentType) > maxContentTypeLen {
		return fmt.Errorf("%w: content type exceeds maximum length", ErrValidation)
	}
	if !utf8.ValidString(contentType) || !isPrintableASCII(contentType) {
		return fmt.Errorf("%w: content type must contain printable ASCII only", ErrValidation)
	}
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil || mediaType == "" || !strings.Contains(mediaType, "/") {
		return fmt.Errorf("%w: invalid content type", ErrValidation)
	}
	return nil
}

func validateCustomMetaKey(key string) error {
	if key == "" {
		return fmt.Errorf("must not be empty")
	}
	if len(key) > maxCustomMetaKeyLen {
		return fmt.Errorf("exceeds maximum length")
	}
	if !isASCIILetterOrDigit(key[0]) || !isASCIILetterOrDigit(key[len(key)-1]) {
		return fmt.Errorf("must start and end with an ASCII letter or digit")
	}
	for i := 0; i < len(key); i++ {
		c := key[i]
		if isASCIILetterOrDigit(c) || c == '-' {
			continue
		}
		return fmt.Errorf("must contain only ASCII letters, digits, or hyphen")
	}
	return nil
}

func isASCIILetterOrDigit(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

func isPrintableASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] > 0x7e {
			return false
		}
	}
	return true
}
