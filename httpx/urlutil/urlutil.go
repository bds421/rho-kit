package urlutil

// Behavior here mirrors github.com/ory/x/urlx (Apache-2.0): trailing-slash
// preservation, RawQuery/Fragment preservation, and treating
// already-percent-encoded segments as opaque. The actual joining is delegated
// to (*url.URL).JoinPath, which has the same semantics.

import (
	"fmt"
	"net/url"
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
		panic(fmt.Sprintf("urlutil: parse %q: %v", base, err))
	}
	return AppendPaths(u, parts...).String()
}

// AppendPaths returns a NEW [*url.URL] with parts joined onto u's path.
// The input u is not mutated. RawQuery and Fragment are preserved.
//
// Empty parts are skipped. A trailing slash on the existing path is kept
// when no segments are appended; a trailing slash on the final part is
// preserved on the result. Already-percent-encoded segments are treated as
// opaque and never double-encoded.
//
// Returns nil when u is nil.
func AppendPaths(u *url.URL, parts ...string) *url.URL {
	if u == nil {
		return nil
	}

	// Drop empty parts so callers can pass conditional segments.
	filtered := make([]string, 0, len(parts))
	for _, p := range parts {
		if p == "" {
			continue
		}
		filtered = append(filtered, p)
	}

	// (*url.URL).JoinPath always returns a fresh URL with its own copy of
	// fields, so it satisfies the no-mutation contract. With zero parts it
	// still returns a copy with the same path (preserving any trailing
	// slash on the base).
	return u.JoinPath(filtered...)
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
		panic(fmt.Sprintf("urlutil: parse request URI %q: %v", s, err))
	}
	return u
}
