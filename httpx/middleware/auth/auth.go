package auth

import (
	"context"
	"crypto/x509"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/bds421/rho-kit/core/contextutil"
	"github.com/bds421/rho-kit/httpx"
	"github.com/bds421/rho-kit/security/jwtutil"
)

// Named types for type-safe, collision-free context keys via contextutil.Key.
type authUserID string
type authScopes string
type permissionSet map[string]struct{}

// trustedS2SMarker is the value type for the trusted-service marker. Its
// presence on the context means the request was authenticated via the mTLS
// S2S branch of RequireS2SAuth and is permitted to bypass RBAC and scope
// checks. Absence means the request must satisfy normal authorization rules.
type trustedS2SMarker struct{}

var (
	userIDKey      contextutil.Key[authUserID]
	permissionsKey contextutil.Key[[]string]
	permSetKey     contextutil.Key[permissionSet]
	scopesKey      contextutil.Key[authScopes]
	trustedS2SKey  contextutil.Key[trustedS2SMarker]
)

// RequireUserWithJWT returns middleware that verifies Oathkeeper-signed JWTs.
// Only Bearer tokens are accepted; X-User-Id header fallback is rejected.
// Use RequireS2SAuth for services that also accept internal S2S calls.
//
// Panics if provider is nil to fail fast on misconfiguration.
func RequireUserWithJWT(provider *jwtutil.Provider) func(http.Handler) http.Handler {
	if provider == nil {
		panic("middleware: RequireUserWithJWT requires a non-nil JWT provider")
	}
	return func(next http.Handler) http.Handler {
		return jwtOnlyHandler(provider, next)
	}
}

// RequireS2SAuth returns middleware that accepts two authentication modes:
//  1. Bearer JWT (same as RequireUserWithJWT)
//  2. mTLS client certificate + X-User-Id header (service-to-service)
//
// For mode 2, the caller's TLS client certificate must satisfy the CN
// allowlist. The TLS layer (ServerTLS with VerifyClientCertIfGiven) verifies
// the cert against the CA; this middleware enforces the per-cert allowlist.
//
// CN-based identity is legacy. CABs deprecate CN as an identity source and
// modern certificate tooling (cert-manager, SPIFFE) emits identities as SANs.
// Prefer [RequireS2SAuthWithIdentity] paired with [WithAllowedSANs] for new
// services; keep this entry point for fleets whose internal CA still issues
// CN-only certs.
//
// Both provider and allowedCNs are required — the function panics at startup
// if either is nil/empty after trimming, making misconfiguration impossible.
//
// An auditor can grep for "RequireS2SAuth" to find all S2S entry points.
func RequireS2SAuth(provider *jwtutil.Provider, allowedCNs []string) func(http.Handler) http.Handler {
	return RequireS2SAuthWithIdentity(provider, WithAllowedCNs(allowedCNs))
}

// MTLSIdentityOption configures the mTLS identity allowlist for
// [RequireS2SAuthWithIdentity]. At least one of [WithAllowedSANs] or
// [WithAllowedCNs] must be supplied with at least one non-empty entry.
type MTLSIdentityOption func(*mtlsIdentityConfig)

type mtlsIdentityConfig struct {
	allowedCNs     map[string]struct{}
	allowedSANDNS  map[string]struct{}
	allowedSANURIs map[string]struct{}
}

// WithAllowedSANs authorises peers whose verified client certificate carries
// at least one DNS SAN or URI SAN matching the provided list. SAN-based
// identities are the modern replacement for CN — SPIFFE IDs and DNS-style
// service names live there — and should be preferred for new deployments.
//
// DNS values (e.g. "svc-a.internal") are matched against [x509.Certificate.DNSNames].
// URI values (e.g. "spiffe://example.org/svc-a") are matched against
// [x509.Certificate.URIs] using exact string equality after URL parsing.
//
// Empty/whitespace entries are skipped.
func WithAllowedSANs(sans []string) MTLSIdentityOption {
	return func(c *mtlsIdentityConfig) {
		for _, s := range sans {
			s = strings.TrimSpace(s)
			if s == "" {
				continue
			}
			if strings.Contains(s, "://") {
				if c.allowedSANURIs == nil {
					c.allowedSANURIs = map[string]struct{}{}
				}
				c.allowedSANURIs[s] = struct{}{}
			} else {
				if c.allowedSANDNS == nil {
					c.allowedSANDNS = map[string]struct{}{}
				}
				c.allowedSANDNS[s] = struct{}{}
			}
		}
	}
}

// WithAllowedCNs authorises peers whose verified client certificate has a
// Subject Common Name matching the provided list. CN identity is legacy;
// new code should pair this with [WithAllowedSANs] or migrate to SANs.
//
// Empty/whitespace entries are skipped.
func WithAllowedCNs(cns []string) MTLSIdentityOption {
	return func(c *mtlsIdentityConfig) {
		for _, cn := range cns {
			cn = strings.TrimSpace(cn)
			if cn == "" {
				continue
			}
			if c.allowedCNs == nil {
				c.allowedCNs = map[string]struct{}{}
			}
			c.allowedCNs[cn] = struct{}{}
		}
	}
}

// RequireS2SAuthWithIdentity is the SAN-aware sibling of [RequireS2SAuth].
// It accepts JWT or mTLS+header authentication; the mTLS branch matches the
// peer certificate against the configured CN/SAN allowlists.
//
// At least one identity allowlist option ([WithAllowedCNs] or
// [WithAllowedSANs]) must contribute at least one non-empty entry.
func RequireS2SAuthWithIdentity(provider *jwtutil.Provider, opts ...MTLSIdentityOption) func(http.Handler) http.Handler {
	if provider == nil {
		panic("middleware: RequireS2SAuthWithIdentity requires a non-nil JWT provider")
	}
	cfg := mtlsIdentityConfig{}
	for _, o := range opts {
		o(&cfg)
	}
	if len(cfg.allowedCNs) == 0 && len(cfg.allowedSANDNS) == 0 && len(cfg.allowedSANURIs) == 0 {
		panic("middleware: RequireS2SAuthWithIdentity requires at least one non-empty allowed CN or SAN entry")
	}
	if len(cfg.allowedCNs) > 0 && len(cfg.allowedSANDNS) == 0 && len(cfg.allowedSANURIs) == 0 {
		slog.Warn("httpx auth middleware: CN-only mTLS allowlist is legacy; prefer WithAllowedSANs",
			slog.String("component", "RequireS2SAuthWithIdentity"),
		)
	}
	return func(next http.Handler) http.Handler {
		return s2sHandler(provider, cfg, next)
	}
}

// jwtOnlyHandler returns a handler that requires a valid Bearer JWT token.
func jwtOnlyHandler(provider *jwtutil.Provider, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := extractBearerToken(r)
		if token == "" {
			httpx.WriteError(w, http.StatusUnauthorized, "unauthorized")
			return
		}

		verifyJWT(w, r, provider, token, next)
	})
}

// s2sHandler returns a handler that accepts JWT or mTLS+header authentication.
func s2sHandler(provider *jwtutil.Provider, cfg mtlsIdentityConfig, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Try JWT from Authorization header first.
		if token := extractBearerToken(r); token != "" {
			verifyJWT(w, r, provider, token, next)
			return
		}

		// Fallback: verify mTLS client certificate identity for S2S auth.
		matched, identity := verifyClientCert(r, cfg)
		if !matched {
			httpx.WriteError(w, http.StatusUnauthorized, "unauthorized")
			return
		}

		requireHeaderUser(w, r, identity, next)
	})
}

// verifyClientCert checks that the request was made over TLS with a fully
// verified client certificate whose SAN URI / SAN DNS / CN is in one of the
// configured allowlists. Match order: SAN URI, SAN DNS, then CN. The first
// match wins; the returned identity string carries the matched value (logged
// for audit). A SAN-only certificate (CN empty) is rejected unless a SAN
// matches.
//
// The VerifiedChains check is essential: r.TLS.PeerCertificates is populated
// any time a peer presents a certificate, even when chain verification was
// skipped or failed. Trusting PeerCertificates without VerifiedChains lets a
// misconfigured proxy (or a tls.Config that omits ClientCAs) admit
// unverified certs. Only trust an identity that the TLS layer itself
// validated against a trusted CA.
func verifyClientCert(r *http.Request, cfg mtlsIdentityConfig) (bool, string) {
	if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 || len(r.TLS.VerifiedChains) == 0 {
		return false, ""
	}
	return matchCertIdentity(r.TLS.PeerCertificates[0], cfg)
}

func matchCertIdentity(cert *x509.Certificate, cfg mtlsIdentityConfig) (bool, string) {
	for _, u := range cert.URIs {
		if u == nil {
			continue
		}
		s := u.String()
		if _, ok := cfg.allowedSANURIs[s]; ok {
			return true, "uri:" + s
		}
	}
	for _, dns := range cert.DNSNames {
		if _, ok := cfg.allowedSANDNS[dns]; ok {
			return true, "dns:" + dns
		}
	}
	if cn := cert.Subject.CommonName; cn != "" {
		if _, ok := cfg.allowedCNs[cn]; ok {
			return true, "cn:" + cn
		}
	}
	return false, ""
}

// verifyJWT validates the token, injects claims into context, and calls next.
// Note: uses time.Now() for token verification. For deterministic testing,
// use jwtutil.NewProviderWithKeySet with pre-built key sets and tokens whose
// expiry window covers the test execution time.
func verifyJWT(w http.ResponseWriter, r *http.Request, provider *jwtutil.Provider, token string, next http.Handler) {
	claims, err := provider.Verify(token, time.Now())
	if err != nil {
		// ErrKeySetUnavailable means the JWKS hasn't been fetched yet or has
		// gone stale; surface the same "unauthorized" response as before but
		// keep the cause distinguishable for logging callers via errors.Is.
		if errors.Is(err, jwtutil.ErrKeySetUnavailable) {
			httpx.WriteError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		httpx.WriteError(w, http.StatusUnauthorized, "invalid token")
		return
	}

	if !jwtutil.IsUUID(claims.Subject) {
		httpx.WriteError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	ctx := userIDKey.Set(r.Context(), authUserID(claims.Subject))
	ctx = permissionsKey.Set(ctx, claims.Permissions)
	// Build a permission map for O(1) lookups in RequirePermission.
	ps := make(permissionSet, len(claims.Permissions))
	for _, p := range claims.Permissions {
		ps[p] = struct{}{}
	}
	ctx = permSetKey.Set(ctx, ps)
	ctx = scopesKey.Set(ctx, authScopes(claims.Scopes))
	next.ServeHTTP(w, r.WithContext(ctx))
}

// requireHeaderUser extracts the user ID from the X-User-Id header for
// mTLS-authenticated S2S requests. Logs the impersonation for audit trail.
//
// The trusted-S2S marker is stamped onto the context here — and ONLY here —
// so that downstream RBAC/scope middleware (RequirePermission,
// PermissionByMethod, RequireScope) can distinguish a verified internal
// caller from a request that simply happens to lack a permissions claim
// (e.g., a JWT minted without the claim, or a route that was misconfigured
// to not run JWT verification at all). Without the marker those middlewares
// fail closed.
func requireHeaderUser(w http.ResponseWriter, r *http.Request, identity string, next http.Handler) {
	userID := r.Header.Get("X-User-Id")
	if userID == "" || !jwtutil.IsUUID(userID) {
		httpx.WriteError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	httpx.Logger(r.Context(), slog.Default()).Info("s2s user impersonation",
		"user_id", userID,
		"client_identity", identity,
		"method", r.Method,
		"path", r.URL.Path,
	)

	ctx := userIDKey.Set(r.Context(), authUserID(userID))
	ctx = trustedS2SKey.Set(ctx, trustedS2SMarker{})
	next.ServeHTTP(w, r.WithContext(ctx))
}

// extractBearerToken returns the token from a "Bearer <token>" Authorization header.
func extractBearerToken(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	if len(auth) > 7 && strings.EqualFold(auth[:7], "bearer ") {
		return strings.TrimSpace(auth[7:])
	}
	return ""
}

// UserID extracts the user ID from the request context.
func UserID(ctx context.Context) string {
	v, _ := userIDKey.Get(ctx)
	return string(v)
}

// Permissions extracts the permissions list from the request context.
// Returns nil if no permissions are available (S2S mTLS auth).
func Permissions(ctx context.Context) []string {
	v, _ := permissionsKey.Get(ctx)
	return v
}

// Scopes extracts the scopes string from the request context.
func Scopes(ctx context.Context) string {
	v, _ := scopesKey.Get(ctx)
	return string(v)
}

// RequirePermission returns middleware that checks the JWT-embedded
// permissions for the required permission string (e.g. "general:view",
// "users:manage").
//
// Fail-closed semantics:
//   - If the trusted-S2S marker is set (request authenticated via the mTLS
//     branch of RequireS2SAuth), the check is bypassed — internal services
//     are trusted explicitly, by virtue of the verified client cert + CN
//     allowlist, not by virtue of "happened to have no permissions claim".
//   - Otherwise the request must carry a permissions set on context
//     (typically from JWT verification) AND the set must contain the
//     required permission. Anything else returns 403.
//
// In particular, a request with no permissions claim and no trusted-S2S
// marker is rejected — this prevents misconfigured routes (auth middleware
// missing in front, JWT issued without the claim) from silently granting
// access.
func RequirePermission(permission string) func(http.Handler) http.Handler {
	if permission == "" {
		// An empty permission silently allows every caller through (the
		// permissions set never contains "" so the check fails-closed,
		// but a typo at wire time should surface at startup, not as a
		// blanket-deny that operators only notice in prod). Match the
		// kit's "refuse to misconfigure" stance.
		panic("auth: RequirePermission requires a non-empty permission name")
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if IsTrustedS2S(r.Context()) {
				next.ServeHTTP(w, r)
				return
			}
			if hasPermissionFast(r.Context(), permission) {
				next.ServeHTTP(w, r)
				return
			}
			httpx.WriteError(w, http.StatusForbidden, "insufficient permissions")
		})
	}
}

// PermissionByMethod returns middleware that selects the required permission
// based on the HTTP method: readPerm for GET/HEAD/OPTIONS, writePerm
// otherwise. Fail-closed semantics match RequirePermission — a request
// without a permissions claim and without the trusted-S2S marker is denied.
func PermissionByMethod(readPerm, writePerm string) func(http.Handler) http.Handler {
	if readPerm == "" {
		panic("auth: PermissionByMethod requires a non-empty readPerm")
	}
	if writePerm == "" {
		panic("auth: PermissionByMethod requires a non-empty writePerm")
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if IsTrustedS2S(r.Context()) {
				next.ServeHTTP(w, r)
				return
			}
			required := writePerm
			switch r.Method {
			case http.MethodGet, http.MethodHead, http.MethodOptions:
				required = readPerm
			}
			if hasPermissionFast(r.Context(), required) {
				next.ServeHTTP(w, r)
				return
			}
			httpx.WriteError(w, http.StatusForbidden, "insufficient permissions")
		})
	}
}

// IsTrustedS2S reports whether ctx carries the trusted service-to-service
// marker. The marker is set only by RequireS2SAuth's mTLS branch after a
// fully verified client certificate with an allow-listed CN. Handlers and
// middleware can use this to grant trust to verified internal callers
// without conflating it with the absence of a permissions claim.
func IsTrustedS2S(ctx context.Context) bool {
	_, ok := trustedS2SKey.Get(ctx)
	return ok
}

// WithTrustedS2S returns ctx marked as a trusted service-to-service caller.
//
// This is intended for use in tests only. Production code must rely on
// RequireS2SAuth's mTLS branch to set the marker after a verified client
// certificate. Setting the marker manually in production would let callers
// bypass RBAC.
func WithTrustedS2S(ctx context.Context) context.Context {
	return trustedS2SKey.Set(ctx, trustedS2SMarker{})
}

// hasPermissionFast checks the pre-built map from context for O(1) lookup.
func hasPermissionFast(ctx context.Context, required string) bool {
	ps, ok := permSetKey.Get(ctx)
	if !ok {
		return false
	}
	_, found := ps[required]
	return found
}

// WithUserID returns a new context with the given user ID.
//
// This is intended for use in tests only. Production code should rely on the
// JWT or mTLS middleware to set the user ID. Using this in production bypasses
// authentication and allows identity spoofing.
func WithUserID(ctx context.Context, id string) context.Context {
	return userIDKey.Set(ctx, authUserID(id))
}

// WithPermissions returns a new context with the given permissions.
//
// This is intended for use in tests only. Production code should rely on the
// JWT middleware to set permissions. Using this in production bypasses
// authorization checks.
func WithPermissions(ctx context.Context, perms []string) context.Context {
	ctx = permissionsKey.Set(ctx, perms)
	ps := make(permissionSet, len(perms))
	for _, p := range perms {
		ps[p] = struct{}{}
	}
	return permSetKey.Set(ctx, ps)
}
