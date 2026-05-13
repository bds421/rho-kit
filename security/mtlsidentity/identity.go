// Package mtlsidentity normalizes mTLS identity allowlist entries.
package mtlsidentity

import (
	"errors"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"
)

var (
	// ErrInvalidSAN is returned when a SAN entry contains unsafe runes
	// (whitespace, control characters, NUL, or non-UTF-8 bytes) that an
	// allowlist comparison must not see.
	ErrInvalidSAN = errors.New("mtlsidentity: invalid SAN")
	// ErrInvalidURISAN is returned when a SAN value looks like a URI but
	// fails to parse, lacks a scheme/host, or carries forbidden parts
	// (userinfo, query, fragment).
	ErrInvalidURISAN = errors.New("mtlsidentity: invalid URI SAN")
	// ErrInvalidDNSSAN is returned when a SAN value is meant to be a DNS
	// name but violates RFC 1035 label rules.
	ErrInvalidDNSSAN = errors.New("mtlsidentity: invalid DNS SAN")
	// ErrInvalidCN is returned when a Common Name contains unsafe runes
	// (whitespace, control characters, NUL, or non-UTF-8 bytes).
	ErrInvalidCN = errors.New("mtlsidentity: invalid CN")
)

// SANKind identifies the form of a Subject Alternative Name entry.
type SANKind int

const (
	// SANDNS marks a DNS-form SAN value (e.g. "svc.cluster.local").
	SANDNS SANKind = iota + 1
	// SANURI marks a URI-form SAN value (e.g. "spiffe://trust/ns/team/sa/svc").
	SANURI
)

// SAN is a normalized Subject Alternative Name entry suitable for
// allowlist comparison. The zero value represents "absent" — see the
// `ok` return from [NormalizeSAN].
type SAN struct {
	Kind  SANKind
	Value string
}

// NormalizeSAN parses an allowlist entry into a [SAN] value. Whitespace
// is trimmed; URI entries (containing "://") are validated and returned
// as [SANURI]; everything else is treated as a DNS name, lower-cased,
// and returned as [SANDNS]. The bool return is false for empty input
// and true for a usable allowlist entry; errors signal a malformed
// entry that must be rejected at config-load time.
func NormalizeSAN(input string) (SAN, bool, error) {
	raw := strings.TrimSpace(input)
	if raw == "" {
		return SAN{}, false, nil
	}
	if containsUnsafeIdentityRune(raw) {
		return SAN{}, false, ErrInvalidSAN
	}
	if strings.Contains(raw, "://") {
		u, err := url.Parse(raw)
		if err != nil || u.Scheme == "" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return SAN{}, false, ErrInvalidURISAN
		}
		return SAN{Kind: SANURI, Value: u.String()}, true, nil
	}
	if !validDNSIdentity(raw) {
		return SAN{}, false, ErrInvalidDNSSAN
	}
	return SAN{Kind: SANDNS, Value: strings.ToLower(raw)}, true, nil
}

// NormalizeCN validates and trims a Common Name allowlist entry.
// The bool return is false for an empty trimmed value and true for a
// usable entry; errors signal a malformed CN that must be rejected at
// config-load time.
func NormalizeCN(input string) (string, bool, error) {
	cn := strings.TrimSpace(input)
	if cn == "" {
		return "", false, nil
	}
	if containsUnsafeIdentityRune(cn) {
		return "", false, ErrInvalidCN
	}
	return cn, true, nil
}

func validDNSIdentity(s string) bool {
	if s == "" || len(s) > 253 || strings.ContainsAny(s, "/\\:*?[]") {
		return false
	}
	for _, label := range strings.Split(s, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for i := 0; i < len(label); i++ {
			c := label[i]
			if ('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z') || ('0' <= c && c <= '9') || c == '-' {
				continue
			}
			return false
		}
	}
	return true
}

func containsUnsafeIdentityRune(s string) bool {
	if !utf8.ValidString(s) {
		return true
	}
	for _, r := range s {
		if r == 0 || unicode.IsSpace(r) || unicode.IsControl(r) {
			return true
		}
	}
	return false
}
