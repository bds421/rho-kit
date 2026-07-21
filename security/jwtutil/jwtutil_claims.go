package jwtutil

import (
	"crypto/tls"
	"regexp"
	"strings"
	"time"
)

// uuidPattern is the canonical UUID matcher shared by httpx and grpcx auth
// middleware so the identity contract ("subject must be a UUID") cannot
// drift between transports.
var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// SubjectPrefixUser is the optional wire prefix for human subject ids. Issuers
// may emit usr_<uuid> instead of a bare UUID; [NormalizeSubjectID] strips it.
const SubjectPrefixUser = "usr_"

// IsUUID reports whether s is a syntactically-valid UUID. Centralised here
// so HTTP and gRPC auth paths apply the same rule to JWT subjects and the
// X-User-Id metadata/header used by mTLS S2S impersonation.
func IsUUID(s string) bool {
	return uuidPattern.MatchString(s)
}

// NormalizeSubjectID accepts a bare UUID or a [SubjectPrefixUser]-prefixed
// subject and returns the canonical UUID. Both HTTP and gRPC auth middleware
// use this so issuer-specific subject formats collapse to one visibility key.
func NormalizeSubjectID(s string) (uuid string, ok bool) {
	if IsUUID(s) {
		return s, true
	}
	if strings.HasPrefix(s, SubjectPrefixUser) {
		uuid = s[len(SubjectPrefixUser):]
		if IsUUID(uuid) {
			return uuid, true
		}
	}
	return "", false
}

const (
	clockSkew              = 30 * time.Second
	defaultRefreshInterval = 10 * time.Minute
	defaultHTTPTimeout     = 5 * time.Second
	defaultMaxStale        = 1 * time.Hour
	minimumTLSVersion      = tls.VersionTLS12
	maxJWTLen              = 16 * 1024
)

// Claims represents the verified JWT payload used by the kit auth middleware.
type Claims struct {
	ID          string   `json:"jti"`
	Subject     string   `json:"sub"`
	Permissions []string `json:"permissions"`
	Scopes      string   `json:"scopes"`
	IssuedAt    int64    `json:"iat"`
	ExpiresAt   int64    `json:"exp"`
	NotBefore   int64    `json:"nbf"`
	Issuer      string   `json:"iss"`

	stringClaims map[string]string
}

// StringClaim returns a string-valued JWT claim captured during verification.
// Standard OAuth claims client_id, azp, and act are always extracted; add more
// via [WithStringClaims] on the [Provider].
func (c *Claims) StringClaim(name string) (string, bool) {
	if c == nil || len(c.stringClaims) == 0 {
		return "", false
	}
	v, ok := c.stringClaims[name]
	return v, ok && v != ""
}
