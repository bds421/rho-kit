package httpx

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/bds421/rho-kit/core/v2/config"
)

// ErrUnsafeRedirect is returned by [SafeRedirect] when a redirect target is
// malformed, externally hosted, or otherwise unsafe to place in a Location
// header.
var ErrUnsafeRedirect = errors.New("httpx: unsafe redirect target")

// SafeRedirect validates rawTarget as a redirect target and returns the
// canonical Location value to pass to http.Redirect.
//
// Relative URLs are allowed only when they stay origin-relative: "/dashboard",
// "settings", "?tab=billing", and "#done" are valid, but "//evil.example" and
// encoded equivalents are rejected. Absolute URLs must use http or https and
// must match one of allowedHosts exactly. Hosts are compared case-insensitively.
// Ports are exact: "example.com" matches "https://example.com/path" while
// "example.com:8443" only matches URLs with that explicit port.
//
// Userinfo, backslashes, controls, raw whitespace, non-HTTP schemes, invalid
// UTF-8, and malformed percent-encoding are rejected. When SafeRedirect returns
// an error, handlers should redirect to a fixed local fallback or write a
// validation error instead of using rawTarget.
func SafeRedirect(rawTarget string, allowedHosts ...string) (string, error) {
	if rawTarget == "" {
		return "", fmt.Errorf("%w: empty target", ErrUnsafeRedirect)
	}
	if strings.TrimSpace(rawTarget) != rawTarget {
		return "", fmt.Errorf("%w: target has surrounding whitespace", ErrUnsafeRedirect)
	}
	if !utf8.ValidString(rawTarget) {
		return "", fmt.Errorf("%w: target is not valid UTF-8", ErrUnsafeRedirect)
	}
	if hasUnsafeRedirectByte(rawTarget) {
		return "", fmt.Errorf("%w: target contains whitespace, controls, or backslashes", ErrUnsafeRedirect)
	}

	u, err := url.Parse(rawTarget)
	if err != nil {
		return "", fmt.Errorf("%w: target URL is invalid", ErrUnsafeRedirect)
	}
	if u.User != nil {
		return "", fmt.Errorf("%w: target must not contain userinfo", ErrUnsafeRedirect)
	}

	if u.Scheme == "" {
		if u.Host != "" {
			return "", fmt.Errorf("%w: scheme-relative target", ErrUnsafeRedirect)
		}
		if isSchemeRelativePath(u) {
			return "", fmt.Errorf("%w: scheme-relative path", ErrUnsafeRedirect)
		}
		return u.String(), nil
	}

	switch strings.ToLower(u.Scheme) {
	case "http", "https":
	default:
		return "", fmt.Errorf("%w: unsupported scheme", ErrUnsafeRedirect)
	}
	if u.Host == "" {
		return "", fmt.Errorf("%w: absolute URL requires a host", ErrUnsafeRedirect)
	}
	if err := config.ValidateURLHost("redirect target", u); err != nil {
		return "", fmt.Errorf("%w: target host is invalid", ErrUnsafeRedirect)
	}
	if !redirectHostAllowed(u, allowedHosts) {
		return "", fmt.Errorf("%w: host is not allowed", ErrUnsafeRedirect)
	}
	return u.String(), nil
}

func hasUnsafeRedirectByte(s string) bool {
	for _, r := range s {
		if r == '\\' || unicode.IsControl(r) || unicode.IsSpace(r) {
			return true
		}
	}
	return false
}

func isSchemeRelativePath(u *url.URL) bool {
	path := u.EscapedPath()
	if path == "" {
		path = u.Path
	}
	if hasSchemeRelativePrefix(path) {
		return true
	}
	unescaped, err := url.PathUnescape(path)
	return err != nil || hasSchemeRelativePrefix(unescaped)
}

func hasSchemeRelativePrefix(path string) bool {
	return len(path) >= 2 && isRedirectSlash(path[0]) && isRedirectSlash(path[1])
}

func isRedirectSlash(b byte) bool {
	return b == '/' || b == '\\'
}

func redirectHostAllowed(u *url.URL, allowedHosts []string) bool {
	targetHost := normalizeRedirectHost(u.Hostname())
	if targetHost == "" {
		return false
	}
	targetPort := u.Port()

	for _, allowed := range allowedHosts {
		host, port, ok := parseAllowedRedirectHost(allowed)
		if !ok || host != targetHost {
			continue
		}
		if port == targetPort {
			return true
		}
	}
	return false
}

func parseAllowedRedirectHost(raw string) (host string, port string, ok bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" || hasUnsafeRedirectByte(raw) || strings.Contains(raw, "://") || strings.ContainsAny(raw, "/@") {
		return "", "", false
	}

	if h, p, err := net.SplitHostPort(raw); err == nil {
		return normalizeRedirectHost(h), p, normalizeRedirectHost(h) != "" && p != ""
	}

	if strings.HasPrefix(raw, "[") && strings.HasSuffix(raw, "]") {
		raw = strings.TrimPrefix(strings.TrimSuffix(raw, "]"), "[")
	}
	return normalizeRedirectHost(raw), "", normalizeRedirectHost(raw) != ""
}

func normalizeRedirectHost(host string) string {
	host = strings.ToLower(strings.TrimSpace(host))
	host = strings.TrimSuffix(host, ".")
	return host
}
