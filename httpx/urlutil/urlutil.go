package urlutil

import (
	"net/url"
	"strings"
	"unicode/utf8"
)

// MustJoin parses base and appends parts to its path, panicking if base does
// not parse. It preserves a trailing slash when base ends in "/" and no
// segments are appended (or when the final part itself ends in "/"),
// preserves base's RawQuery and Fragment, and avoids re-encoding
// already-percent-encoded segments.
//
// Use this for redirect targets, signed-URL templates, and webhook callback
// URLs where the inputs are static or already-validated. For untrusted
// input, parse explicitly and propagate the error.
func MustJoin(base string, parts ...string) string {
	u, err := url.Parse(base)
	if err != nil {
		panic("urlutil: MustJoin base URL is invalid")
	}
	return AppendPaths(u, parts...).String()
}

// AppendPaths returns a NEW [*url.URL] with parts joined onto u's path.
// The input u is not mutated. RawQuery and Fragment are preserved.
//
// Empty parts are skipped. Each non-empty part is appended as one opaque path
// segment; pass multiple parts when you want multiple path levels. Leading
// slashes in a part are ignored so a part cannot replace u's existing path, and
// trailing slashes are preserved as a trailing slash on the result.
// Already-percent-encoded octets are preserved and never double-encoded,
// except when preserving them would decode to path separators or "." / ".."
// path segments.
//
// Returns nil when u is nil.
func AppendPaths(u *url.URL, parts ...string) *url.URL {
	if u == nil {
		return nil
	}

	filtered := make([]string, 0, len(parts))
	for _, p := range parts {
		if p == "" {
			continue
		}
		filtered = append(filtered, p)
	}
	out := Copy(u)
	if len(filtered) == 0 {
		return out
	}

	escapedPath := out.EscapedPath()
	for _, part := range filtered {
		trailingSlash := strings.HasSuffix(part, "/")
		trimmed := strings.TrimLeft(part, "/")
		trimmed = strings.TrimRight(trimmed, "/")
		if trimmed == "" {
			if trailingSlash {
				if escapedPath == "" {
					escapedPath = "/"
				} else if !strings.HasSuffix(escapedPath, "/") {
					escapedPath += "/"
				}
			}
			continue
		}

		if escapedPath == "" {
			escapedPath = "/" + escapeOpaquePathSegment(trimmed)
		} else if strings.HasSuffix(escapedPath, "/") {
			escapedPath += escapeOpaquePathSegment(trimmed)
		} else {
			escapedPath += "/" + escapeOpaquePathSegment(trimmed)
		}
		if trailingSlash && !strings.HasSuffix(escapedPath, "/") {
			escapedPath += "/"
		}
	}

	setEscapedPath(out, escapedPath)
	return out
}

// Copy returns a deep copy of u. The returned URL is safe to mutate without
// affecting the input. Returns nil when u is nil.
func Copy(u *url.URL) *url.URL {
	if u == nil {
		return nil
	}
	cp := *u
	if u.User != nil {
		// url.Userinfo is opaque; reconstruct via the same factories so the
		// copy does not alias the input's *Userinfo.
		if pw, ok := u.User.Password(); ok {
			cp.User = url.UserPassword(u.User.Username(), pw)
		} else {
			cp.User = url.User(u.User.Username())
		}
	}
	return &cp
}

// ParseRequestURIOrPanic wraps [url.ParseRequestURI] and panics on parse
// error. Use only for compile-time-known URLs (defaults, fixtures); never
// for client input.
func ParseRequestURIOrPanic(s string) *url.URL {
	u, err := url.ParseRequestURI(s)
	if err != nil {
		panic("urlutil: ParseRequestURIOrPanic request URI is invalid")
	}
	return u
}

func setEscapedPath(u *url.URL, escapedPath string) {
	path, err := url.PathUnescape(escapedPath)
	if err != nil {
		u.Path = escapedPath
		u.RawPath = ""
		return
	}
	u.Path = path

	defaultEscaped := (&url.URL{Path: path}).EscapedPath()
	if escapedPath == defaultEscaped {
		u.RawPath = ""
	} else {
		u.RawPath = escapedPath
	}
}

func escapeOpaquePathSegment(s string) string {
	var b strings.Builder
	preserveEscapes := !containsDecodedPathControl(s)
	for i := 0; i < len(s); {
		if preserveEscapes && s[i] == '%' && i+2 < len(s) && isHex(s[i+1]) && isHex(s[i+2]) {
			b.WriteString(s[i : i+3])
			i += 3
			continue
		}

		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			b.WriteString(url.PathEscape(s[i : i+1]))
			i++
			continue
		}
		if r == '.' && isDotSegmentByte(s, i, size) {
			b.WriteString("%2E")
		} else {
			b.WriteString(url.PathEscape(s[i : i+size]))
		}
		i += size
	}
	return b.String()
}

func containsDecodedPathControl(s string) bool {
	decoded := make([]byte, 0, len(s))
	for i := 0; i < len(s); {
		if s[i] == '%' && i+2 < len(s) && isHex(s[i+1]) && isHex(s[i+2]) {
			b := fromHex(s[i+1])<<4 | fromHex(s[i+2])
			if b == '/' || b == '\\' {
				return true
			}
			decoded = append(decoded, b)
			i += 3
			continue
		}
		decoded = append(decoded, s[i])
		i++
	}

	start := 0
	for i := 0; i <= len(decoded); i++ {
		if i != len(decoded) && decoded[i] != '/' && decoded[i] != '\\' {
			continue
		}
		seg := decoded[start:i]
		if len(seg) == 1 && seg[0] == '.' {
			return true
		}
		if len(seg) == 2 && seg[0] == '.' && seg[1] == '.' {
			return true
		}
		start = i + 1
	}
	return false
}

func isDotSegmentByte(s string, i, size int) bool {
	before := i == 0 || s[i-1] == '/'
	after := i+size == len(s) || s[i+size] == '/'
	if before && after {
		return true
	}

	if i+size < len(s) && s[i+size] == '.' {
		secondEnd := i + size + 1
		return before && (secondEnd == len(s) || s[secondEnd] == '/')
	}
	if i > 0 && s[i-1] == '.' {
		firstStart := i - 1
		return (firstStart == 0 || s[firstStart-1] == '/') && after
	}
	return false
}

func isHex(b byte) bool {
	return ('0' <= b && b <= '9') || ('a' <= b && b <= 'f') || ('A' <= b && b <= 'F')
}

func fromHex(b byte) byte {
	switch {
	case '0' <= b && b <= '9':
		return b - '0'
	case 'a' <= b && b <= 'f':
		return b - 'a' + 10
	default:
		return b - 'A' + 10
	}
}
