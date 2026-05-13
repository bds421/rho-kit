package interceptor

import (
	"context"
	"crypto/x509"
	"errors"
	"log/slog"
	"runtime/debug"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/bds421/rho-kit/core/v2/contextutil"
	"github.com/bds421/rho-kit/core/v2/redact"
	"github.com/bds421/rho-kit/security/v2/jwtutil"
	"github.com/bds421/rho-kit/security/v2/mtlsidentity"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

// Named types for type-safe context keys.
type grpcUserID string
type grpcPermissions []string
type grpcScopes string

// grpcTrustedS2SMarker is the value type for the trusted-service marker. Its
// presence on the context means the request was authenticated via the mTLS
// S2S branch of MTLSAuthUnary/MTLSAuthStream and is permitted to bypass
// RBAC and scope checks. Absence means the request must satisfy normal
// authorization rules. Mirrors the semantics of httpx/middleware/auth.
type grpcTrustedS2SMarker struct{}

var (
	userIDKey      = contextutil.NewKey[grpcUserID]("grpcx.auth.user_id")
	permissionsKey = contextutil.NewKey[grpcPermissions]("grpcx.auth.permissions")
	scopesKey      = contextutil.NewKey[grpcScopes]("grpcx.auth.scopes")
	trustedS2SKey  = contextutil.NewKey[grpcTrustedS2SMarker]("grpcx.auth.trusted_s2s")
)

// xUserIDMetadataKey is the gRPC metadata key carrying the impersonated
// user ID on mTLS S2S calls. Lower-cased to match gRPC metadata canonicalisation.
const xUserIDMetadataKey = "x-user-id"

// AuthOption configures the auth interceptor.
type AuthOption func(*authConfig)

type authConfig struct {
	skipMethods map[string]struct{}
}

// WithSkipMethods specifies gRPC methods that should bypass authentication.
// Method names should be fully qualified (e.g., "/grpc.health.v1.Health/Check").
func WithSkipMethods(methods ...string) AuthOption {
	copied := append([]string(nil), methods...)
	return func(c *authConfig) {
		for _, m := range copied {
			c.skipMethods[m] = struct{}{}
		}
	}
}

// AuthUnary returns a unary server interceptor that extracts and validates
// JWT tokens from the "authorization" gRPC metadata key.
//
// The token format is "Bearer <token>" matching the HTTP convention. On
// success, the user ID, permissions, and scopes from the JWT claims are
// injected into the context.
//
// Panics if provider is nil to fail fast on misconfiguration.
func AuthUnary(provider *jwtutil.Provider, opts ...AuthOption) grpc.UnaryServerInterceptor {
	if provider == nil {
		panic("interceptor: AuthUnary requires a non-nil JWT provider")
	}
	cfg := buildAuthConfig(opts)
	return func(
		ctx context.Context,
		req any,
		info *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (any, error) {
		if _, skip := cfg.skipMethods[info.FullMethod]; skip {
			return handler(ctx, req)
		}
		ctx, err := authenticate(ctx, provider)
		if err != nil {
			return nil, err
		}
		return handler(ctx, req)
	}
}

// AuthStream returns a stream server interceptor that validates JWT tokens.
func AuthStream(provider *jwtutil.Provider, opts ...AuthOption) grpc.StreamServerInterceptor {
	if provider == nil {
		panic("interceptor: AuthStream requires a non-nil JWT provider")
	}
	cfg := buildAuthConfig(opts)
	return func(
		srv any,
		ss grpc.ServerStream,
		info *grpc.StreamServerInfo,
		handler grpc.StreamHandler,
	) error {
		if _, skip := cfg.skipMethods[info.FullMethod]; skip {
			return handler(srv, ss)
		}
		ctx, err := authenticate(ss.Context(), provider)
		if err != nil {
			return err
		}
		return handler(srv, &contextStream{ServerStream: ss, ctx: ctx})
	}
}

// buildAuthConfig applies options and returns the resulting configuration.
func buildAuthConfig(opts []AuthOption) authConfig {
	cfg := authConfig{
		skipMethods: make(map[string]struct{}),
	}
	for _, opt := range opts {
		if opt == nil {
			panic("interceptor: auth option must not be nil")
		}
		opt(&cfg)
	}
	return cfg
}

// authenticate extracts the JWT from metadata, validates it, and injects
// claims into the context.
func authenticate(ctx context.Context, provider *jwtutil.Provider) (context.Context, error) {
	token, tokenStatus := parseBearerToken(ctx)
	switch tokenStatus {
	case bearerTokenAbsent:
		return ctx, status.Error(codes.Unauthenticated, "missing authorization token")
	case bearerTokenInvalid:
		return ctx, status.Error(codes.Unauthenticated, "invalid authorization token")
	case bearerTokenPresent:
	default:
		return ctx, status.Error(codes.Unauthenticated, "invalid authorization token")
	}
	return authenticateBearer(ctx, provider, token)
}

func authenticateBearer(ctx context.Context, provider *jwtutil.Provider, token string) (context.Context, error) {
	claims, err := provider.VerifyContext(ctx, token, time.Now())
	if err != nil {
		// ErrKeySetUnavailable is the JWKS-not-loaded / stale case; emit a
		// warning so it is distinguishable in logs from a malformed token.
		if errors.Is(err, jwtutil.ErrKeySetUnavailable) {
			slog.WarnContext(ctx, "grpc auth: JWKS not yet loaded")
			return ctx, status.Error(codes.Unauthenticated, "authorization unavailable")
		}
		return ctx, status.Error(codes.Unauthenticated, "invalid token")
	}

	// Same subject contract as httpx/middleware/auth: a non-UUID sub
	// must not flow into authorization or business logic as a user id.
	if !jwtutil.IsUUID(claims.Subject) {
		return ctx, status.Error(codes.Unauthenticated, "invalid token")
	}

	perms := slices.Clone(claims.Permissions)
	ctx = userIDKey.Set(ctx, grpcUserID(claims.Subject))
	ctx = permissionsKey.Set(ctx, grpcPermissions(perms))
	ctx = scopesKey.Set(ctx, grpcScopes(claims.Scopes))
	return ctx, nil
}

type bearerTokenStatus int

const (
	bearerTokenAbsent bearerTokenStatus = iota
	bearerTokenInvalid
	bearerTokenPresent
)

const maxBearerTokenLen = 8 * 1024

// parseBearerToken reads a singleton "authorization" metadata value and
// strips the "Bearer " prefix. Multiple metadata values are rejected because
// proxies and frameworks can disagree about which value is authoritative.
func parseBearerToken(ctx context.Context) (string, bearerTokenStatus) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return "", bearerTokenAbsent
	}
	vals := md.Get("authorization")
	switch len(vals) {
	case 0:
		return "", bearerTokenAbsent
	case 1:
	default:
		return "", bearerTokenInvalid
	}

	auth := vals[0]
	if auth == "" || strings.TrimSpace(auth) != auth || !utf8.ValidString(auth) {
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

// UserID extracts the user ID from the gRPC request context.
func UserID(ctx context.Context) string {
	v, _ := userIDKey.Get(ctx)
	return string(v)
}

// UserPermissions extracts the permissions list from the gRPC request context.
func UserPermissions(ctx context.Context) []string {
	v, _ := permissionsKey.Get(ctx)
	return slices.Clone([]string(v))
}

// UserScopes extracts the scopes string from the gRPC request context.
func UserScopes(ctx context.Context) string {
	v, _ := scopesKey.Get(ctx)
	return string(v)
}

// IsTrustedS2S reports whether ctx carries the trusted service-to-service
// marker. The marker is set only by MTLSAuthUnary/MTLSAuthStream's mTLS
// branch after a fully verified client certificate with an allow-listed
// SAN URI/DNS or (legacy) CN identity. Handlers and authorization
// interceptors can use this to grant trust to verified internal callers
// without conflating it with the absence of a permissions claim. Mirrors
// the semantics of httpx/middleware/auth.IsTrustedS2S.
func IsTrustedS2S(ctx context.Context) bool {
	_, ok := trustedS2SKey.Get(ctx)
	return ok
}

// RequirePermissionUnary returns a unary server interceptor that enforces a
// permission check using the permissions slot populated by AuthUnary (or
// MTLSAuthUnary). Fail-closed semantics mirror
// httpx/middleware/auth.RequirePermission:
//
//   - If [IsTrustedS2S] returns true, the check is bypassed — internal
//     services authenticated via verified mTLS + SAN/CN allowlist are
//     trusted explicitly, not by virtue of "happened to have no permissions
//     claim".
//   - Otherwise the request must carry permissions on context AND that set
//     must contain perm. Anything else returns codes.PermissionDenied.
//
// Panics if perm is empty to fail fast on misconfiguration — an empty
// required permission is almost always a coding error and would either
// pass-by-accident if the JWT carried an empty-string permission or
// fail-by-default for everyone, both of which mask the bug.
func RequirePermissionUnary(perm string) grpc.UnaryServerInterceptor {
	if perm == "" {
		panic("interceptor: RequirePermissionUnary requires a non-empty permission")
	}
	return func(
		ctx context.Context,
		req any,
		_ *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (any, error) {
		if !checkPermission(ctx, perm) {
			return nil, status.Error(codes.PermissionDenied, "insufficient permissions")
		}
		return handler(ctx, req)
	}
}

// RequirePermissionStream returns a stream server interceptor that enforces
// a permission check. See [RequirePermissionUnary] for semantics.
func RequirePermissionStream(perm string) grpc.StreamServerInterceptor {
	if perm == "" {
		panic("interceptor: RequirePermissionStream requires a non-empty permission")
	}
	return func(
		srv any,
		ss grpc.ServerStream,
		_ *grpc.StreamServerInfo,
		handler grpc.StreamHandler,
	) error {
		if !checkPermission(ss.Context(), perm) {
			return status.Error(codes.PermissionDenied, "insufficient permissions")
		}
		return handler(srv, ss)
	}
}

// RequireScopeUnary returns a unary server interceptor that checks the
// space-separated scopes string populated by AuthUnary for the required
// scope. Fail-closed: a request without scopes on context AND without the
// trusted-S2S marker is rejected with codes.PermissionDenied.
//
// Panics if scope is empty.
func RequireScopeUnary(scope string) grpc.UnaryServerInterceptor {
	if scope == "" {
		panic("interceptor: RequireScopeUnary requires a non-empty scope")
	}
	return func(
		ctx context.Context,
		req any,
		_ *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (any, error) {
		if !checkScope(ctx, scope) {
			return nil, status.Error(codes.PermissionDenied, "insufficient scope")
		}
		return handler(ctx, req)
	}
}

// RequireScopeStream returns a stream server interceptor that enforces a
// scope check. See [RequireScopeUnary] for semantics.
func RequireScopeStream(scope string) grpc.StreamServerInterceptor {
	if scope == "" {
		panic("interceptor: RequireScopeStream requires a non-empty scope")
	}
	return func(
		srv any,
		ss grpc.ServerStream,
		_ *grpc.StreamServerInfo,
		handler grpc.StreamHandler,
	) error {
		if !checkScope(ss.Context(), scope) {
			return status.Error(codes.PermissionDenied, "insufficient scope")
		}
		return handler(srv, ss)
	}
}

// MTLSIdentityOption configures the mTLS identity allowlist and impersonation
// policy for [MTLSAuthUnary] / [MTLSAuthStream]. At least one of
// [WithAllowedSANs] or [WithAllowedCNs] must be supplied.
type MTLSIdentityOption func(*mtlsIdentityConfig)

type mtlsIdentityConfig struct {
	allowedCNs     map[string]struct{}
	allowedSANDNS  map[string]struct{}
	allowedSANURIs map[string]struct{}
	// impersonationGuard is consulted before stamping the trusted-S2S
	// marker. Returning an error rejects the impersonation attempt;
	// returning nil accepts it.
	impersonationGuard func(ctx context.Context, identity, userID string) error
	// skipMethods bypasses both JWT and mTLS authentication for the
	// listed gRPC method names (audit FR-066). Use for unauthenticated
	// health endpoints under a combined JWT/mTLS interceptor.
	skipMethods map[string]struct{}
}

// WithMTLSSkipMethods bypasses authentication for the listed gRPC
// method names (audit FR-066). Mirrors [WithSkipMethods] for the
// JWT-only [AuthUnary] / [AuthStream] interceptors so services using
// the combined JWT + mTLS auth can still expose unauthenticated
// health endpoints.
func WithMTLSSkipMethods(methods ...string) MTLSIdentityOption {
	copied := append([]string(nil), methods...)
	return func(c *mtlsIdentityConfig) {
		if c.skipMethods == nil {
			c.skipMethods = make(map[string]struct{})
		}
		for _, m := range copied {
			c.skipMethods[m] = struct{}{}
		}
	}
}

// WithS2SImpersonationGuard installs a callback that decides whether
// `identity` (the verified client certificate's matched SAN/CN) is
// allowed to impersonate `userID`. Return an error to reject the
// impersonation; the interceptor returns codes.PermissionDenied to
// the caller.
func WithS2SImpersonationGuard(fn func(ctx context.Context, identity, userID string) error) MTLSIdentityOption {
	if fn == nil {
		panic("interceptor: WithS2SImpersonationGuard requires a non-nil callback")
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
func WithAllowedSANs(sans ...string) MTLSIdentityOption {
	dnsSANs := make([]string, 0, len(sans))
	uriSANs := make([]string, 0, len(sans))
	for _, s := range sans {
		san, ok, err := mtlsidentity.NormalizeSAN(s)
		if err != nil {
			switch {
			case errors.Is(err, mtlsidentity.ErrInvalidURISAN):
				panic("interceptor: WithAllowedSANs invalid URI SAN")
			case errors.Is(err, mtlsidentity.ErrInvalidDNSSAN):
				panic("interceptor: WithAllowedSANs invalid DNS SAN")
			default:
				panic("interceptor: WithAllowedSANs invalid SAN")
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
// Subject Common Name matching the provided list.
//
// CN-based identity is a legacy concept; CABs deprecate CN as an identity
// source and modern certificate tooling (cert-manager, SPIFFE) emits
// identities as SANs. This option remains for fleets whose internal CA still
// issues CN-only certs. New code should pair it with [WithAllowedSANs] or
// migrate to SANs entirely — a runtime warning is logged at startup when CN
// is the sole identity source.
func WithAllowedCNs(cns ...string) MTLSIdentityOption {
	canonical := make([]string, 0, len(cns))
	for _, input := range cns {
		cn, ok, err := mtlsidentity.NormalizeCN(input)
		if err != nil {
			panic("interceptor: WithAllowedCNs invalid CN")
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

func buildMTLSIdentityConfig(opts []MTLSIdentityOption) mtlsIdentityConfig {
	cfg := mtlsIdentityConfig{}
	for _, o := range opts {
		if o == nil {
			panic("interceptor: mTLS identity option must not be nil")
		}
		o(&cfg)
	}
	return cfg
}

// MTLSAuthUnary returns a unary server interceptor that accepts two
// authentication modes:
//
//  1. Bearer JWT (same as AuthUnary).
//  2. mTLS client certificate + x-user-id metadata (service-to-service).
//
// For mode 2, the caller's verified client certificate must satisfy at
// least one allow-listed identity (SAN DNS, SAN URI, or CN). The
// certificate chain must have been verified by the gRPC TLS layer. The
// middleware reads x-user-id from incoming metadata, validates it as a
// UUID, and stamps a trusted-S2S marker on the context so downstream
// RequirePermissionUnary / RequireScopeUnary interceptors permit the call
// without a permissions claim.
//
// Provider, at least one identity option, and WithS2SImpersonationGuard are
// required; the function panics at startup if any are missing.
//
// An auditor can grep for "MTLSAuthUnary" to find all S2S entry points.
func MTLSAuthUnary(provider *jwtutil.Provider, opts ...MTLSIdentityOption) grpc.UnaryServerInterceptor {
	if provider == nil {
		panic("interceptor: MTLSAuthUnary requires a non-nil JWT provider")
	}
	cfg := buildMTLSIdentityConfig(opts)
	if len(cfg.allowedCNs) == 0 && len(cfg.allowedSANDNS) == 0 && len(cfg.allowedSANURIs) == 0 {
		panic("interceptor: MTLSAuthUnary requires at least one allowed SAN or CN")
	}
	if cfg.impersonationGuard == nil {
		panic("interceptor: MTLSAuthUnary requires WithS2SImpersonationGuard for mTLS user impersonation")
	}
	warnIfCNOnly("MTLSAuthUnary", cfg)
	return func(
		ctx context.Context,
		req any,
		info *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (any, error) {
		if _, skip := cfg.skipMethods[info.FullMethod]; skip {
			return handler(ctx, req)
		}
		newCtx, err := authenticateMTLSOrJWT(ctx, provider, cfg)
		if err != nil {
			return nil, err
		}
		return handler(newCtx, req)
	}
}

// MTLSAuthStream returns a stream server interceptor that accepts JWT or
// mTLS+metadata authentication. See [MTLSAuthUnary] for semantics.
func MTLSAuthStream(provider *jwtutil.Provider, opts ...MTLSIdentityOption) grpc.StreamServerInterceptor {
	if provider == nil {
		panic("interceptor: MTLSAuthStream requires a non-nil JWT provider")
	}
	cfg := buildMTLSIdentityConfig(opts)
	if len(cfg.allowedCNs) == 0 && len(cfg.allowedSANDNS) == 0 && len(cfg.allowedSANURIs) == 0 {
		panic("interceptor: MTLSAuthStream requires at least one allowed SAN or CN")
	}
	if cfg.impersonationGuard == nil {
		panic("interceptor: MTLSAuthStream requires WithS2SImpersonationGuard for mTLS user impersonation")
	}
	warnIfCNOnly("MTLSAuthStream", cfg)
	return func(
		srv any,
		ss grpc.ServerStream,
		info *grpc.StreamServerInfo,
		handler grpc.StreamHandler,
	) error {
		if _, skip := cfg.skipMethods[info.FullMethod]; skip {
			return handler(srv, ss)
		}
		newCtx, err := authenticateMTLSOrJWT(ss.Context(), provider, cfg)
		if err != nil {
			return err
		}
		return handler(srv, &contextStream{ServerStream: ss, ctx: newCtx})
	}
}

// warnIfCNOnly logs a startup warning when CN is the only configured
// identity source. CN is deprecated as an identity carrier; operators
// should migrate to SAN DNS/URI before EOLing the CN allowlist.
func warnIfCNOnly(component string, cfg mtlsIdentityConfig) {
	if len(cfg.allowedCNs) > 0 && len(cfg.allowedSANDNS) == 0 && len(cfg.allowedSANURIs) == 0 {
		slog.Warn("grpcx interceptor: CN-only mTLS allowlist is legacy; prefer WithAllowedSANs",
			slog.String("component", component),
		)
	}
}

// authenticateMTLSOrJWT tries JWT bearer auth first; on absence, falls
// back to verified-mTLS + identity allowlist + x-user-id metadata. The
// marker is stamped only on the mTLS branch, never on the JWT branch.
func authenticateMTLSOrJWT(
	ctx context.Context,
	provider *jwtutil.Provider,
	cfg mtlsIdentityConfig,
) (context.Context, error) {
	// Try JWT first. Malformed or duplicated authorization metadata is
	// rejected before mTLS fallback so callers cannot smuggle a bad bearer
	// value while relying on a client certificate for admission.
	token, tokenStatus := parseBearerToken(ctx)
	switch tokenStatus {
	case bearerTokenPresent:
		return authenticateBearer(ctx, provider, token)
	case bearerTokenInvalid:
		return ctx, status.Error(codes.Unauthenticated, "invalid authorization token")
	case bearerTokenAbsent:
	}

	// mTLS branch: require a verified client cert with an allow-listed identity.
	matched, identity := verifyClientCertGRPC(ctx, cfg)
	if !matched {
		return ctx, status.Error(codes.Unauthenticated, "unauthorized")
	}

	userID, ok := extractXUserID(ctx)
	if !ok || !jwtutil.IsUUID(userID) {
		return ctx, status.Error(codes.Unauthenticated, "unauthorized")
	}

	if cfg.impersonationGuard != nil {
		if err := callImpersonationGuard(ctx, cfg.impersonationGuard, identity, userID); err != nil {
			slog.WarnContext(ctx, "grpc s2s impersonation rejected by guard",
				slog.String("user_id", userID),
				slog.String("client_identity", identity),
				redact.Error(err),
			)
			return ctx, status.Error(codes.PermissionDenied, "impersonation not permitted")
		}
	}

	slog.InfoContext(ctx, "grpc s2s user impersonation",
		slog.String("user_id", userID),
		slog.String("client_identity", identity),
	)

	ctx = userIDKey.Set(ctx, grpcUserID(userID))
	ctx = trustedS2SKey.Set(ctx, grpcTrustedS2SMarker{})
	return ctx, nil
}

var errImpersonationGuardPanicked = errors.New("impersonation guard panicked")

func callImpersonationGuard(ctx context.Context, guard func(context.Context, string, string) error, identity, userID string) (err error) {
	defer func() {
		if rec := recover(); rec != nil {
			slog.ErrorContext(ctx, "grpc s2s impersonation guard panicked",
				slog.String("user_id", userID),
				slog.String("client_identity", identity),
				redact.Panic(rec),
				slog.String("stack", string(debug.Stack())),
			)
			err = errImpersonationGuardPanicked
		}
	}()
	return guard(ctx, identity, userID)
}

// verifyClientCertGRPC checks that the gRPC peer presented a fully verified
// client certificate whose SAN DNS / SAN URI / CN is in the allowlist.
//
// The VerifiedChains check is essential: PeerCertificates is populated any
// time a peer presents a certificate, even when chain verification was
// skipped or failed. Trusting PeerCertificates without VerifiedChains lets
// a misconfigured server (ClientAuth=RequestClientCert without ClientCAs)
// admit unverified certs. Only trust an identity that the TLS layer itself
// validated against a trusted CA. Mirrors the HTTP verifyClientCert.
//
// Match order: SAN URI, SAN DNS, then CN. The first match wins; the
// returned identity string is the matched value (logged for audit). When
// no identity matches, returns ("", "").
func verifyClientCertGRPC(ctx context.Context, cfg mtlsIdentityConfig) (bool, string) {
	p, ok := peer.FromContext(ctx)
	if !ok {
		return false, ""
	}
	tlsInfo, ok := p.AuthInfo.(credentials.TLSInfo)
	if !ok {
		return false, ""
	}
	if len(tlsInfo.State.PeerCertificates) == 0 || len(tlsInfo.State.VerifiedChains) == 0 {
		return false, ""
	}
	cert := tlsInfo.State.PeerCertificates[0]
	if cert.IsCA {
		return false, ""
	}
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

// extractXUserID reads a singleton x-user-id metadata value from the incoming
// context. Returns ok=false when absent, duplicated, blank, or ambiguous.
func extractXUserID(ctx context.Context) (value string, ok bool) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return "", false
	}
	return singletonMetadataIdentity(md, xUserIDMetadataKey)
}

func singletonMetadataIdentity(md metadata.MD, key string) (string, bool) {
	vals := md.Get(key)
	if len(vals) != 1 {
		return "", false
	}
	value := vals[0]
	if value == "" || strings.TrimSpace(value) != value {
		return "", false
	}
	if !utf8.ValidString(value) || strings.Contains(value, ",") {
		return "", false
	}
	for _, r := range value {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return "", false
		}
	}
	return value, true
}

// checkPermission applies the fail-closed RBAC predicate: trusted-S2S
// bypass, otherwise the permission set must contain perm.
func checkPermission(ctx context.Context, perm string) bool {
	if IsTrustedS2S(ctx) {
		return true
	}
	perms, ok := permissionsKey.Get(ctx)
	if !ok {
		return false
	}
	return slices.Contains([]string(perms), perm)
}

// checkScope applies the fail-closed scope predicate: trusted-S2S
// bypass, otherwise the space-separated scopes claim must contain scope
// as a whole token.
func checkScope(ctx context.Context, scope string) bool {
	if IsTrustedS2S(ctx) {
		return true
	}
	scopes, ok := scopesKey.Get(ctx)
	if !ok {
		return false
	}
	for _, s := range strings.Fields(string(scopes)) {
		if s == scope {
			return true
		}
	}
	return false
}
