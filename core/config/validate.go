package config

import (
	"net/url"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/bds421/rho-kit/core/v2/redact"
)

// ValidatePort checks that a port number is in the valid 1–65535 range.
func ValidatePort(name string, port int) error {
	if port < 1 || port > 65535 {
		return redact.Safef("invalid %s port", name)
	}
	return nil
}

// ValidateURLHost checks that a parsed URL has a usable network host.
//
// Use this after scheme-specific checks for service URLs loaded from config.
// It rejects empty hostnames, malformed explicit ports, zone identifiers, and
// whitespace/control characters so clients fail at startup instead of passing
// surprising authority strings to network libraries.
func ValidateURLHost(name string, u *url.URL) error {
	if u == nil {
		return redact.Safef("%s URL must not be nil", name)
	}
	if u.Host == "" {
		return redact.Safef("%s must include a host", name)
	}
	host := u.Hostname()
	if host == "" {
		return redact.Safef("%s must include a host name", name)
	}
	if !utf8.ValidString(host) {
		return redact.Safef("%s host must be valid UTF-8", name)
	}
	if strings.ContainsRune(host, ':') && !strings.HasPrefix(u.Host, "[") {
		return redact.Safef("%s host contains invalid unbracketed IPv6 or port syntax", name)
	}
	if strings.ContainsAny(host, `%/\`) {
		return redact.Safef("%s host must not contain percent-encoding, zone identifiers, or path separators", name)
	}
	for _, r := range host {
		if r == 0 || unicode.IsControl(r) || unicode.IsSpace(r) {
			return redact.Safef("%s host contains whitespace or control characters", name)
		}
	}
	port := u.Port()
	if port == "" {
		if urlHostHasPortSeparator(u.Host) {
			return redact.Safef("%s port is invalid", name)
		}
		return nil
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return redact.Safef("%s port is invalid", name)
	}
	return nil
}

func urlHostHasPortSeparator(host string) bool {
	if strings.HasPrefix(host, "[") {
		end := strings.LastIndex(host, "]")
		return end >= 0 && len(host) > end+1 && host[end+1] == ':'
	}
	return strings.Count(host, ":") == 1
}

// IsDevelopment reports whether the environment string indicates development mode.
// The ENVIRONMENT variable must be explicitly set to "development" to enable
// development features such as debug endpoints.
func IsDevelopment(environment string) bool {
	return environment == "development"
}

// RejectWeakCredential returns an error if the value is too short or contains "changeme".
// Use this in production mode to prevent deployment with default credentials.
func RejectWeakCredential(name, value string) error {
	if len(value) < 12 {
		return redact.Safef("%s must be at least 12 characters long", name)
	}
	if strings.Contains(strings.ToLower(value), "changeme") {
		return redact.Safef("%s contains 'changeme' — replace with a strong credential before running in production", name)
	}
	return nil
}

// ValidatePositive returns an error if value is not a positive integer.
func ValidatePositive(name string, value int) error {
	if value <= 0 {
		return redact.Safef("%s must be positive", name)
	}
	return nil
}
