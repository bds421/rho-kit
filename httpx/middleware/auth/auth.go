// asvs: V2.1.5, V2.3.1, V3.2.1, V3.3.1, V4.1.1, V4.1.5
package auth

import (
	"context"
	"crypto/x509"
	"errors"
	"log/slog"
	"net/http"
	"runtime/debug"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/net/http/httpguts"

	"github.com/bds421/rho-kit/core/v2/contextutil"
	"github.com/bds421/rho-kit/core/v2/redact"
	"github.com/bds421/rho-kit/httpx/v2"
	"github.com/bds421/rho-kit/httpx/v2/internal/headerutil"
	"github.com/bds421/rho-kit/security/v2/jwtutil"
	"github.com/bds421/rho-kit/security/v2/mtlsidentity"
)

// Named types for type-safe, collision-free context keys via contextutil.Key.
type authUserID string
type authSubject string
type authActor string
type authTenant string
type authRole string
type authScopes string
type permissionSet map[string]struct{}

// trustedS2SMarker is the value type for the trusted-service marker. Its
// presence on the context means the request was authenticated via the mTLS
// S2S branch of RequireS2SAuth and is permitted to bypass RBAC and scope
// checks. Absence means the request must satisfy normal authorization rules.
type trustedS2SMarker struct{}

var (
	userIDKey      = contextutil.NewKey[authUserID]("httpx.auth.user_id")
	subjectKey     = contextutil.NewKey[authSubject]("httpx.auth.subject")
	actorKey       = contextutil.NewKey[authActor]("httpx.auth.actor")
	actorKindKey   = contextutil.NewKey[ActorKind]("httpx.auth.actor_kind")
	tenantKey      = contextutil.NewKey[authTenant]("httpx.auth.tenant")
	roleKey        = contextutil.NewKey[authRole]("httpx.auth.role")
	permissionsKey = contextutil.NewKey[[]string]("httpx.auth.permissions")
	permSetKey     = contextutil.NewKey[permissionSet]("httpx.auth.permission_set")
	scopesKey      = contextutil.NewKey[authScopes]("httpx.auth.scopes")
	trustedS2SKey  = contextutil.NewKey[trustedS2SMarker]("httpx.auth.trusted_s2s")
)

// JWT returns chain-shape middleware that verifies Bearer JWTs through
// provider. Only Bearer tokens are accepted; X-User-Id header fallback is
// rejected. Use [RequireS2SAuth] for services that also accept internal
// service-to-service calls.
//
// Panics if provider is nil to fail fast on misconfiguration.
func JWT(provider *jwtutil.Provider) func(http.Handler) http.Handler {
	if provider == nil {
		panic("middleware/auth: JWT requires a non-nil JWT provider")
	}
	return func(next http.Handler) http.Handler {
		return jwtOnlyHandler(provider, next)
	}
}

// RequireS2SAuth returns middleware that accepts two authentication modes:
//  1. Bearer JWT (same as [JWT])
//  2. mTLS client certificate + X-User-Id header (service-to-service)
//
// For mode 2, the caller's TLS client certificate must satisfy the CN
// allowlist and the X-User-Id value must be permitted by a
// WithS2SImpersonationGuard callback. The TLS layer verifies the cert against
// the CA; this middleware enforces the per-cert allowlist.
//
// CN-based identity is legacy. CABs deprecate CN as an identity source and
// modern certificate tooling (cert-manager, SPIFFE) emits identities as SANs.
// Prefer [RequireS2SAuthWithIdentity] paired with [WithAllowedSANs] for new
// services; keep this entry point for fleets whose internal CA still issues
// CN-only certs.
//
// The provider, allowedCNs, and WithS2SImpersonationGuard option are required;
// the function panics at startup if any are missing.
//
// An auditor can grep for "RequireS2SAuth" to find all S2S entry points.
func RequireS2SAuth(provider *jwtutil.Provider, allowedCNs []string, opts ...MTLSIdentityOption) func(http.Handler) http.Handler {
	all := make([]MTLSIdentityOption, 0, len(opts)+1)
	all = append(all, WithAllowedCNs(allowedCNs...))
	all = append(all, opts...)
	return RequireS2SAuthWithIdentity(provider, all...)
}

// MTLSIdentityOption configures the mTLS identity allowlist and impersonation
// policy for [RequireS2SAuthWithIdentity]. At least one of [WithAllowedSANs]
// or [WithAllowedCNs] must be supplied with at least one non-empty entry.
type MTLSIdentityOption func(*mtlsIdentityConfig)

type mtlsIdentityConfig struct {
	allowedCNs     map[string]struct{}
	allowedSANDNS  map[string]struct{}
	allowedSANURIs map[string]struct{}
	// impersonationGuard is consulted before stamping the trusted-S2S
	// marker. Returning an error rejects the impersonation.
	impersonationGuard func(r *http.Request, identity, userID string) error
}

// WithS2SImpersonationGuard installs a callback that decides whether
// `identity` is allowed to impersonate `userID`. Returning an error
// rejects the impersonation; the middleware returns 403.
//
// The `identity` value is the matched certificate identity prefixed with
// the SAN/CN kind that matched, NOT the bare service name:
//   - "cn:<common-name>"  — e.g. "cn:backend"
//   - "dns:<dns-san>"     — e.g. "dns:svc-a.internal"
//   - "uri:<uri-san>"     — e.g. "uri:spiffe://example.org/svc-a"
//
// A guard that compares against bare names (identity == "backend") will
// reject every request — a silent fail-closed outage. Compare against the
// prefixed form (identity == "cn:backend"), or strip the prefix before
// matching.
func WithS2SImpersonationGuard(fn func(r *http.Request, identity, userID string) error) MTLSIdentityOption {
	if fn == nil {
		panic("middleware/auth: WithS2SImpersonationGuard requires a non-nil callback")
	}
	return func(c *mtlsIdentityConfig) { c.impersonationGuard = fn }
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
func WithAllowedSANs(sans ...string) MTLSIdentityOption {
	dnsSANs := make([]string, 0, len(sans))
	uriSANs := make([]string, 0, len(sans))
	for _, s := range sans {
		san, ok, err := mtlsidentity.NormalizeSAN(s)
		if err != nil {
			switch {
			case errors.Is(err, mtlsidentity.ErrInvalidURISAN):
				panic("middleware/auth: WithAllowedSANs invalid URI SAN")
			case errors.Is(err, mtlsidentity.ErrInvalidDNSSAN):
				panic("middleware/auth: WithAllowedSANs invalid DNS SAN")
			default:
				panic("middleware/auth: WithAllowedSANs invalid SAN")
			}
		}
		if !ok {
			continue
		}
		switch san.Kind {
		case mtlsidentity.SANURI:
			uriSANs = append(uriSANs, san.Value)
		case mtlsidentity.SANDNS:
			dnsSANs = append(dnsSANs, san.Value)
		}
	}
	return func(c *mtlsIdentityConfig) {
		for _, uri := range uriSANs {
			if c.allowedSANURIs == nil {
				c.allowedSANURIs = map[string]struct{}{}
			}
			c.allowedSANURIs[uri] = struct{}{}
		}
		for _, dns := range dnsSANs {
			if c.allowedSANDNS == nil {
				c.allowedSANDNS = map[string]struct{}{}
			}
			c.allowedSANDNS[dns] = struct{}{}
		}
	}
}

// WithAllowedCNs authorises peers whose verified client certificate has a
// Subject Common Name matching the provided list. CN identity is legacy;
// new code should pair this with [WithAllowedSANs] or migrate to SANs.
//
// Empty/whitespace entries are skipped.
func WithAllowedCNs(cns ...string) MTLSIdentityOption {
	canonical := make([]string, 0, len(cns))
	for _, input := range cns {
		cn, ok, err := mtlsidentity.NormalizeCN(input)
		if err != nil {
			panic("middleware/auth: WithAllowedCNs invalid CN")
		}
		if ok {
			canonical = append(canonical, cn)
		}
	}
	return func(c *mtlsIdentityConfig) {
		for _, cn := range canonical {
			if c.allowedCNs == nil {
				c.allowedCNs = map[string]struct{}{}
			}
			c.allowedCNs[cn] = struct{}{}
		}
	}
}

// RequireS2SAuthWithIdentity is the SAN-aware sibling of [RequireS2SAuth].
// It accepts JWT or mTLS+header authentication; the mTLS branch matches the
// peer certificate against the configured CN/SAN allowlists, then requires a
// WithS2SImpersonationGuard callback to approve the requested X-User-Id.
//
// At least one identity allowlist option ([WithAllowedCNs] or
// [WithAllowedSANs]) must contribute at least one non-empty entry, and
// [WithS2SImpersonationGuard] must be supplied.
//
// The identity passed to the impersonation guard is prefixed with the
// matched kind ("cn:", "dns:", or "uri:") — see [WithS2SImpersonationGuard]
// for the exact convention a guard must compare against.
func RequireS2SAuthWithIdentity(provider *jwtutil.Provider, opts ...MTLSIdentityOption) func(http.Handler) http.Handler {
	if provider == nil {
		panic("middleware/auth: RequireS2SAuthWithIdentity requires a non-nil JWT provider")
	}
	cfg := mtlsIdentityConfig{}
	for _, o := range opts {
		if o == nil {
			panic("middleware/auth: RequireS2SAuthWithIdentity option must not be nil")
		}
		o(&cfg)
	}
	if len(cfg.allowedCNs) == 0 && len(cfg.allowedSANDNS) == 0 && len(cfg.allowedSANURIs) == 0 {
		panic("middleware/auth: RequireS2SAuthWithIdentity requires at least one non-empty allowed CN or SAN entry")
	}
	if cfg.impersonationGuard == nil {
		panic("middleware/auth: RequireS2SAuthWithIdentity requires WithS2SImpersonationGuard for mTLS user impersonation")
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

// writeBearerUnauthorized writes a 401 carrying the RFC 7235 WWW-Authenticate
// challenge with the Bearer scheme (RFC 6750). Standard HTTP clients and SDKs
// key their re-auth behaviour off this header; bearer-token auth failures must
// advertise the scheme the endpoint expects.
func writeBearerUnauthorized(w http.ResponseWriter, msg string) {
	w.Header().Set("WWW-Authenticate", "Bearer")
	httpx.WriteError(w, http.StatusUnauthorized, msg)
}

// jwtOnlyHandler returns a handler that requires a valid Bearer JWT token.
func jwtOnlyHandler(provider *jwtutil.Provider, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, status := parseBearerToken(r)
		if status != bearerTokenPresent {
			writeBearerUnauthorized(w, "unauthorized")
			return
		}

		verifyJWT(w, r, provider, token, next)
	})
}

// s2sHandler returns a handler that accepts JWT or mTLS+header authentication.
func s2sHandler(provider *jwtutil.Provider, cfg mtlsIdentityConfig, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Try JWT from Authorization header first. If an Authorization
		// header is present but malformed or duplicated, fail closed instead
		// of falling back to mTLS and changing auth mode mid-request.
		token, status := parseBearerToken(r)
		switch status {
		case bearerTokenPresent:
			verifyJWT(w, r, provider, token, next)
			return
		case bearerTokenInvalid:
			writeBearerUnauthorized(w, "unauthorized")
			return
		}

		// Fallback: verify mTLS client certificate identity for S2S auth.
		matched, identity := verifyClientCert(r, cfg)
		if !matched {
			httpx.WriteError(w, http.StatusUnauthorized, "unauthorized")
			return
		}

		requireHeaderUser(w, r, identity, cfg.impersonationGuard, next)
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
	// Refuse CA certificates outright. A CA leaf presented as a client
	// cert would otherwise bypass the SAN/CN allowlist if the operator's
	// CA is sloppy enough to issue one with a matching CN.
	if cert.IsCA {
		return false, ""
	}
	// Require ExtKeyUsage to include ClientAuth. Server-auth-only certs
	// (or certs with no EKU at all) must not authenticate clients —
	// this catches mis-issued certs that the operator's CA chained-of-trust
	// happens to accept.
	hasClientAuth := false
	for _, eku := range cert.ExtKeyUsage {
		if eku == x509.ExtKeyUsageClientAuth || eku == x509.ExtKeyUsageAny {
			hasClientAuth = true
			break
		}
	}
	if !hasClientAuth {
		return false, ""
	}

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
		if _, ok := cfg.allowedSANDNS[strings.ToLower(dns)]; ok {
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
	claims, err := provider.VerifyContext(r.Context(), token, time.Now())
	if err != nil {
		// ErrKeySetUnavailable means the JWKS hasn't been fetched yet or has
		// gone stale; surface the same "unauthorized" response as before but
		// keep the cause distinguishable for logging callers via errors.Is.
		if errors.Is(err, jwtutil.ErrKeySetUnavailable) {
			writeBearerUnauthorized(w, "unauthorized")
			return
		}
		writeBearerUnauthorized(w, "invalid token")
		return
	}

	subject, ok := jwtutil.NormalizeSubjectID(claims.Subject)
	if !ok {
		writeBearerUnauthorized(w, "unauthorized")
		return
	}

	ctx := stampIdentity(r.Context(), Identity{
		Subject:     subject,
		Actor:       subject,
		ActorKind:   ActorUser,
		Permissions: slices.Clone(claims.Permissions),
		Scopes:      claims.Scopes,
	})
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
func requireHeaderUser(w http.ResponseWriter, r *http.Request, identity string, guard func(*http.Request, string, string) error, next http.Handler) {
	rawUserID, ok := singleHeaderValue(r.Header, "X-User-Id")
	if !ok {
		httpx.WriteError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	userID, ok := jwtutil.NormalizeSubjectID(rawUserID)
	if !ok {
		httpx.WriteError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	if guard != nil {
		if err := callImpersonationGuard(guard, r, identity, userID); err != nil {
			httpx.Logger(r.Context(), slog.Default()).Warn("s2s impersonation rejected by guard",
				redact.String("user_id", userID),
				redact.String("client_identity", identity),
				redact.Error(err),
			)
			httpx.WriteError(w, http.StatusForbidden, "impersonation not permitted")
			return
		}
	}

	httpx.Logger(r.Context(), slog.Default()).Info("s2s user impersonation",
		redact.String("user_id", userID),
		redact.String("client_identity", identity),
		"method", r.Method,
		redact.String("path", httpx.RequestPath(r)),
	)

	ctx := stampIdentity(r.Context(), Identity{
		Subject:   userID,
		Actor:     identity,
		ActorKind: ActorService,
		Trusted:   true,
	})
	next.ServeHTTP(w, r.WithContext(ctx))
}

var errImpersonationGuardPanicked = errors.New("impersonation guard panicked")

func callImpersonationGuard(guard func(*http.Request, string, string) error, r *http.Request, identity, userID string) (err error) {
	defer func() {
		if rec := recover(); rec != nil {
			httpx.Logger(r.Context(), slog.Default()).Error("s2s impersonation guard panicked",
				redact.String("user_id", userID),
				redact.String("client_identity", identity),
				redact.Panic(rec),
				"stack", string(debug.Stack()),
			)
			err = errImpersonationGuardPanicked
		}
	}()
	return guard(r, identity, userID)
}

type bearerTokenStatus int

const (
	bearerTokenAbsent bearerTokenStatus = iota
	bearerTokenInvalid
	bearerTokenPresent
)

const maxBearerTokenLen = 8 * 1024

// parseBearerToken returns the token from a singleton "Bearer <token>"
// Authorization header. Multiple header instances are rejected because
// proxies and frameworks can disagree about which value wins.
func parseBearerToken(r *http.Request) (string, bearerTokenStatus) {
	values := r.Header.Values("Authorization")
	switch len(values) {
	case 0:
		return "", bearerTokenAbsent
	case 1:
	default:
		return "", bearerTokenInvalid
	}

	auth := values[0]
	if auth == "" || strings.TrimSpace(auth) != auth || !utf8.ValidString(auth) || !httpguts.ValidHeaderFieldValue(auth) {
		return "", bearerTokenInvalid
	}
	const bearerPrefix = "Bearer "
	if len(auth) <= len(bearerPrefix) || !strings.EqualFold(auth[:len(bearerPrefix)], bearerPrefix) {
		return "", bearerTokenInvalid
	}
	token := auth[len(bearerPrefix):]
	if !validBearerToken(token) {
		return "", bearerTokenInvalid
	}
	return token, bearerTokenPresent
}

func validBearerToken(token string) bool {
	if token == "" || len(token) > maxBearerTokenLen || strings.TrimSpace(token) != token || strings.Contains(token, ",") {
		return false
	}
	for _, r := range token {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func singleHeaderValue(h http.Header, name string) (string, bool) {
	return headerutil.SingletonIdentity(h, name)
}

// Subject extracts the visibility subject UUID from context. Falls back to the
// legacy user_id key when only JWT/mTLS paths stamped [UserID].
func Subject(ctx context.Context) string {
	v, ok := subjectKey.Get(ctx)
	if ok && v != "" {
		return string(v)
	}
	return UserID(ctx)
}

// Actor extracts the attribution id (key id, client id, or user UUID).
func Actor(ctx context.Context) string {
	v, _ := actorKey.Get(ctx)
	return string(v)
}

// ActorKindFromContext returns the actor classification stamped by auth middleware.
func ActorKindFromContext(ctx context.Context) ActorKind {
	v, _ := actorKindKey.Get(ctx)
	return v
}

// IsMachine reports whether the request was authenticated as a non-human actor.
func IsMachine(ctx context.Context) bool {
	return IsMachineKind(ActorKindFromContext(ctx))
}

// UserID extracts the subject UUID from the request context. Deprecated: use
// [Subject]; reads the legacy user_id key or subject key.
func UserID(ctx context.Context) string {
	v, _ := userIDKey.Get(ctx)
	return string(v)
}

// Tenant extracts the tenant id stamped by multi-credential auth strategies.
func Tenant(ctx context.Context) string {
	v, _ := tenantKey.Get(ctx)
	return string(v)
}

// Role extracts the coarse RBAC role stamped by session or scoped-key auth.
func Role(ctx context.Context) string {
	v, _ := roleKey.Get(ctx)
	return string(v)
}

// Permissions extracts the permissions list from the request context.
// Returns nil if no permissions are available (S2S mTLS auth).
func Permissions(ctx context.Context) []string {
	v, _ := permissionsKey.Get(ctx)
	return slices.Clone(v)
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

// hasPermissionFast checks the pre-built map from context for O(1) lookup.
func hasPermissionFast(ctx context.Context, required string) bool {
	ps, ok := permSetKey.Get(ctx)
	if !ok {
		return false
	}
	_, found := ps[required]
	return found
}
