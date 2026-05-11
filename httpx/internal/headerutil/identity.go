package headerutil

import (
	"net/http"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/net/http/httpguts"
)

// SingletonToken returns the only value for a trust-boundary token header,
// plus whether the header was present and whether the present value was valid.
//
// This is intentionally stricter than generic HTTP header handling: actor,
// user, subject, and tenant headers are trust-boundary identifiers, not
// comma-joinable lists or free text. Rejecting comma-combined values prevents
// proxy/framework disagreement about whether "alice,bob" is one identity or
// two identities.
func SingletonToken(h http.Header, name string) (value string, present bool, ok bool) {
	values := h.Values(name)
	if len(values) == 0 {
		return "", false, false
	}
	if len(values) != 1 {
		return "", true, false
	}
	value = values[0]
	if value == "" || strings.TrimSpace(value) != value {
		return "", true, false
	}
	if !utf8.ValidString(value) || !httpguts.ValidHeaderFieldValue(value) || strings.Contains(value, ",") {
		return "", true, false
	}
	for _, r := range value {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return "", true, false
		}
	}
	return value, true, true
}

// SingletonIdentity returns the only value for an identity-bearing header.
func SingletonIdentity(h http.Header, name string) (string, bool) {
	value, present, ok := SingletonToken(h, name)
	if !present || !ok {
		return "", false
	}
	return value, true
}
