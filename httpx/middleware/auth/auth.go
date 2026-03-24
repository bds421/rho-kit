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

var (
	userIDKey      contextutil.Key[authUserID]
	permissionsKey contextutil.Key[[]string]
	permSetKey     contextutil.Key[permissionSet]
	scopesKey      contextutil.Key[authScopes]
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

// verifyClientCert checks that the request was made over TLS with a verified
// client certificate whose CN is in the allowlist. The TLS layer already
// verified the certificate chain against the CA (VerifyClientCertIfGiven);
// this function only checks the identity.
func verifyClientCert(r *http.Request, allowedCNs map[string]struct{}) bool {
	if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
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
// Note: no permissions are injected into context — RequirePermission and
// PermissionByMethod will see nil permissions and skip RBAC checks. This is
// intentional: mTLS-authenticated internal services are trusted.
func requireHeaderUser(w http.ResponseWriter, r *http.Request, next http.Handler) {
	userID := r.Header.Get("X-User-Id")
	if userID == "" || !uuidPattern.MatchString(userID) {
		httpx.WriteError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	// Audit log: record which service (cert CN) is acting on behalf of which user.
	// This enables forensic analysis if an upstream service is compromised.
	cn := ""
	if r.TLS != nil && len(r.TLS.PeerCertificates) > 0 {
		cn = r.TLS.PeerCertificates[0].Subject.CommonName
	}
	httpx.Logger(r.Context(), slog.Default()).Info("s2s user impersonation",
		"user_id", userID,
		"client_cn", cn,
		"method", r.Method,
		"path", r.URL.Path,
	)

	ctx := userIDKey.Set(r.Context(), authUserID(userID))
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

// RequirePermission returns middleware that checks the JWT-embedded permissions
// for the required permission string (e.g. "general:view", "users:manage").
// When permissions are nil (internal S2S calls via mTLS), the check is
// skipped — mTLS-authenticated internal calls are trusted.
func RequirePermission(permission string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			perms := Permissions(r.Context())
			if perms == nil {
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
// based on the HTTP method: readPerm for GET/HEAD/OPTIONS, writePerm otherwise.
func PermissionByMethod(readPerm, writePerm string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			perms := Permissions(r.Context())
			if perms == nil {
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
