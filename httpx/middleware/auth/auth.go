package auth

import (
	"context"
	"log/slog"
	"net/http"
	"regexp"
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

// uuidPattern matches a standard UUID string (v4, v7, etc.).
var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

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
// For mode 2, the caller's TLS client certificate CN must be in allowedCNs.
// The TLS layer (ServerTLS with VerifyClientCertIfGiven) verifies the cert
// against the CA; this middleware only checks the CN allowlist.
//
// Both provider and allowedCNs are required — the function panics at startup
// if either is nil/empty, making misconfiguration impossible.
//
// An auditor can grep for "RequireS2SAuth" to find all S2S entry points.
func RequireS2SAuth(provider *jwtutil.Provider, allowedCNs []string) func(http.Handler) http.Handler {
	if provider == nil {
		panic("middleware: RequireS2SAuth requires a non-nil JWT provider")
	}
	if len(allowedCNs) == 0 {
		panic("middleware: RequireS2SAuth requires at least one allowed CN")
	}
	cnSet := make(map[string]struct{}, len(allowedCNs))
	for _, cn := range allowedCNs {
		cnSet[cn] = struct{}{}
	}
	return func(next http.Handler) http.Handler {
		return s2sHandler(provider, cnSet, next)
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
func s2sHandler(provider *jwtutil.Provider, allowedCNs map[string]struct{}, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Try JWT from Authorization header first.
		if token := extractBearerToken(r); token != "" {
			verifyJWT(w, r, provider, token, next)
			return
		}

		// Fallback: verify mTLS client certificate CN for S2S auth.
		if !verifyClientCert(r, allowedCNs) {
			httpx.WriteError(w, http.StatusUnauthorized, "unauthorized")
			return
		}

		requireHeaderUser(w, r, next)
	})
}

// verifyClientCert checks that the request was made over TLS with a fully
// verified client certificate whose CN is in the allowlist.
//
// The VerifiedChains check is essential: r.TLS.PeerCertificates is populated
// any time a peer presents a certificate, even when chain verification was
// skipped or failed. Trusting PeerCertificates without VerifiedChains lets a
// misconfigured proxy (or a tls.Config that omits ClientCAs) admit
// unverified certs. Only trust an identity that the TLS layer itself
// validated against a trusted CA.
func verifyClientCert(r *http.Request, allowedCNs map[string]struct{}) bool {
	if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 || len(r.TLS.VerifiedChains) == 0 {
		return false
	}
	cn := r.TLS.PeerCertificates[0].Subject.CommonName
	_, ok := allowedCNs[cn]
	return ok
}

// verifyJWT validates the token, injects claims into context, and calls next.
// Note: uses time.Now() for token verification. For deterministic testing,
// use jwtutil.NewProviderWithKeySet with pre-built key sets and tokens whose
// expiry window covers the test execution time.
func verifyJWT(w http.ResponseWriter, r *http.Request, provider *jwtutil.Provider, token string, next http.Handler) {
	ks := provider.KeySet()
	if ks == nil {
		httpx.WriteError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	claims, err := ks.Verify(token, time.Now())
	if err != nil {
		httpx.WriteError(w, http.StatusUnauthorized, "invalid token")
		return
	}

	if !uuidPattern.MatchString(claims.Subject) {
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
func requireHeaderUser(w http.ResponseWriter, r *http.Request, next http.Handler) {
	userID := r.Header.Get("X-User-Id")
	if userID == "" || !uuidPattern.MatchString(userID) {
		httpx.WriteError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	// Precondition: this function is only reached after verifyClientCert
	// returned true, which already enforced len(r.TLS.VerifiedChains) > 0.
	// Reading PeerCertificates here is safe because the chain was actually
	// verified upstream — but keep that contract close at hand: if a
	// future refactor splits verifyClientCert from this function and
	// PeerCertificates can be reached without a chain check, this code
	// must re-add `len(r.TLS.VerifiedChains) > 0`.
	cn := ""
	if r.TLS != nil && len(r.TLS.PeerCertificates) > 0 && len(r.TLS.VerifiedChains) > 0 {
		cn = r.TLS.PeerCertificates[0].Subject.CommonName
	}
	httpx.Logger(r.Context(), slog.Default()).Info("s2s user impersonation",
		"user_id", userID,
		"client_cn", cn,
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
