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
	ErrInvalidSAN    = errors.New("mtlsidentity: invalid SAN")
	ErrInvalidURISAN = errors.New("mtlsidentity: invalid URI SAN")
	ErrInvalidDNSSAN = errors.New("mtlsidentity: invalid DNS SAN")
	ErrInvalidCN     = errors.New("mtlsidentity: invalid CN")
)

type SANKind int

const (
	SANDNS SANKind = iota + 1
	SANURI
)

type SAN struct {
	Kind  SANKind
	Value string
}

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
