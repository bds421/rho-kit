package httpx

import (
	"net/http"

	"github.com/google/uuid"

	"github.com/bds421/rho-kit/core/v2/apperror"
)

// RequestPath returns the escaped URL path for request-derived metadata. It is
// nil-safe and falls back to URL.Path only when EscapedPath is unavailable.
func RequestPath(r *http.Request) string {
	if r == nil || r.URL == nil {
		return ""
	}
	if escaped := r.URL.EscapedPath(); escaped != "" {
		return escaped
	}
	return r.URL.Path
}

// ParsePathID extracts a path parameter and validates it as a UUID.
// Returns the ID string and true on success, or writes a structured
// validation error and returns false if the value is not a valid UUID.
//
// Only the canonical UUID form (lowercase, hyphenated, 36 characters) is
// accepted. Non-canonical variants that uuid.Parse otherwise tolerates —
// "urn:uuid:" prefixes, braced "{...}", raw 32-hex, and uppercase — are
// rejected so that distinct path strings cannot address the same logical
// resource (which would split caches, audit logs, and string-keyed lookups).
func ParsePathID(w http.ResponseWriter, r *http.Request, param string) (string, bool) {
	if r == nil {
		writePathIDValidationError(w, param)
		return "", false
	}
	raw := r.PathValue(param)
	parsed, err := uuid.Parse(raw)
	if err != nil || parsed.String() != raw {
		writePathIDValidationError(w, param)
		return "", false
	}
	return raw, true
}

func writePathIDValidationError(w http.ResponseWriter, param string) {
	if w == nil {
		return
	}
	WriteValidationError(w, nil, apperror.NewFieldValidation(apperror.FieldError{
		Field:   param,
		Message: "invalid UUID format",
	}))
}
