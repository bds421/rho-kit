package auth

import (
	"context"
	"net/http"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/net/http/httpguts"
)

// Outbound / inbound HTTP headers for trusted-S2S entitlement propagation.
// Values are stamped only from verified auth context (or re-validated on
// the inbound mTLS path) — never copy unverified caller headers through
// untrusted hops.
const (
	HeaderPermissions = "X-Permissions"
	HeaderScopes      = "X-Scopes"
)

// maxOutgoingEntitlements bounds how many permission/scope tokens a single
// hop may stamp so a hostile claim cannot inflate per-request headers.
const maxOutgoingEntitlements = 64

// maxOutgoingEntitlementBytes bounds a single permission/scope token.
const maxOutgoingEntitlementBytes = 256

// AppendOutgoingIdentity copies Subject and user entitlements from ctx onto
// an outbound HTTP request for trusted service-to-service calls. Sets
// X-User-Id, X-Permissions, and X-Scopes when not already present.
//
// Pair with RequireS2SAuth on the receiving side: mTLS admission adopts
// X-Permissions / X-Scopes into context so RequirePermission / RequireScope
// still enforce the original user's entitlements without WithTrustedS2SBypass.
//
// Only call this when dialing a trusted peer over mTLS. Do not attach
// entitlements to untrusted or third-party endpoints.
func AppendOutgoingIdentity(ctx context.Context, req *http.Request) {
	if req == nil {
		return
	}
	if subj := Subject(ctx); subj != "" && req.Header.Get("X-User-Id") == "" {
		req.Header.Set("X-User-Id", subj)
	}
	if perms := Permissions(ctx); len(perms) > 0 && req.Header.Get(HeaderPermissions) == "" {
		if safe := filterSafeEntitlements(perms); len(safe) > 0 {
			req.Header.Set(HeaderPermissions, strings.Join(safe, " "))
		}
	}
	if scopes := Scopes(ctx); scopes != "" && req.Header.Get(HeaderScopes) == "" {
		tokens := splitEntitlementHeader(scopes)
		if safe := filterSafeEntitlements(tokens); len(safe) > 0 {
			req.Header.Set(HeaderScopes, strings.Join(safe, " "))
		}
	}
}

// entitlementsFromHeaders parses optional X-Permissions / X-Scopes for a
// trusted-S2S mTLS admission. Invalid tokens are dropped; a missing header
// yields empty entitlements (fail-closed at Require*).
func entitlementsFromHeaders(h http.Header) (perms []string, scopes string) {
	if raw, ok := spaceSeparatedHeader(h, HeaderPermissions); ok {
		perms = filterSafeEntitlements(splitEntitlementHeader(raw))
	}
	if raw, ok := spaceSeparatedHeader(h, HeaderScopes); ok {
		if safe := filterSafeEntitlements(splitEntitlementHeader(raw)); len(safe) > 0 {
			scopes = strings.Join(safe, " ")
		}
	}
	return perms, scopes
}

// spaceSeparatedHeader returns a singleton header value that may contain
// spaces (unlike SingletonIdentity, which rejects whitespace). Duplicate
// lines and comma-combined values are rejected.
func spaceSeparatedHeader(h http.Header, name string) (string, bool) {
	values := h.Values(name)
	if len(values) != 1 {
		return "", false
	}
	value := values[0]
	if value == "" || strings.TrimSpace(value) != value {
		return "", false
	}
	if !utf8.ValidString(value) || !httpguts.ValidHeaderFieldValue(value) {
		return "", false
	}
	if strings.Contains(value, ",") {
		return "", false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return "", false
		}
	}
	return value, true
}

func splitEntitlementHeader(raw string) []string {
	return strings.Fields(raw)
}

func filterSafeEntitlements(vals []string) []string {
	if len(vals) == 0 {
		return nil
	}
	out := make([]string, 0, len(vals))
	for _, v := range vals {
		if !isSafeEntitlementToken(v) {
			continue
		}
		out = append(out, v)
		if len(out) >= maxOutgoingEntitlements {
			break
		}
	}
	return out
}

func isSafeEntitlementToken(v string) bool {
	if v == "" || len(v) > maxOutgoingEntitlementBytes {
		return false
	}
	if strings.TrimSpace(v) != v {
		return false
	}
	if !utf8.ValidString(v) || strings.Contains(v, ",") {
		return false
	}
	for _, r := range v {
		if r > unicode.MaxASCII || unicode.IsSpace(r) || unicode.IsControl(r) {
			return false
		}
	}
	return true
}
