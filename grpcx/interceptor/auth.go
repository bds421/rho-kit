package interceptor

import (
	"context"
	"log/slog"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/bds421/rho-kit/core/contextutil"
	"github.com/bds421/rho-kit/security/jwtutil"
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
	userIDKey      contextutil.Key[grpcUserID]
	permissionsKey contextutil.Key[grpcPermissions]
	scopesKey      contextutil.Key[grpcScopes]
	trustedS2SKey  contextutil.Key[grpcTrustedS2SMarker]
)

// uuidPattern matches a standard UUID string (v4, v7, etc.). Mirrors the
// pattern used by httpx/middleware/auth so the X-User-Id metadata value
// validated on the mTLS S2S branch is shaped the same as the HTTP version.
var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

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
	return func(c *authConfig) {
		for _, m := range methods {
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
		opt(&cfg)
	}
	return cfg
}

// authenticate extracts the JWT from metadata, validates it, and injects
// claims into the context.
func authenticate(ctx context.Context, provider *jwtutil.Provider) (context.Context, error) {
	token := extractBearerToken(ctx)
	if token == "" {
		return ctx, status.Error(codes.Unauthenticated, "missing authorization token")
	}

	ks := provider.KeySet()
	if ks == nil {
		slog.WarnContext(ctx, "grpc auth: JWKS not yet loaded")
		return ctx, status.Error(codes.Unauthenticated, "authorization unavailable")
	}

	claims, err := ks.Verify(token, time.Now())
	if err != nil {
		return ctx, status.Error(codes.Unauthenticated, "invalid token")
	}

	ctx = userIDKey.Set(ctx, grpcUserID(claims.Subject))
	ctx = permissionsKey.Set(ctx, grpcPermissions(claims.Permissions))
	ctx = scopesKey.Set(ctx, grpcScopes(claims.Scopes))
	return ctx, nil
}

// extractBearerToken reads the "authorization" metadata value and strips the
// "Bearer " prefix.
func extractBearerToken(ctx context.Context) string {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ""
	}
	vals := md.Get("authorization")
	if len(vals) == 0 {
		return ""
	}
	auth := vals[0]
	if len(auth) > 7 && strings.EqualFold(auth[:7], "bearer ") {
		return strings.TrimSpace(auth[7:])
	}
	return ""
}

// UserID extracts the user ID from the gRPC request context.
func UserID(ctx context.Context) string {
	v, _ := userIDKey.Get(ctx)
	return string(v)
}

// UserPermissions extracts the permissions list from the gRPC request context.
func UserPermissions(ctx context.Context) []string {
	v, _ := permissionsKey.Get(ctx)
	return []string(v)
}

// UserScopes extracts the scopes string from the gRPC request context.
func UserScopes(ctx context.Context) string {
	v, _ := scopesKey.Get(ctx)
	return string(v)
}

// IsTrustedS2S reports whether ctx carries the trusted service-to-service
// marker. The marker is set only by MTLSAuthUnary/MTLSAuthStream's mTLS
// branch after a fully verified client certificate with an allow-listed CN.
// Handlers and authorization interceptors can use this to grant trust to
// verified internal callers without conflating it with the absence of a
// permissions claim. Mirrors the semantics of httpx/middleware/auth.IsTrustedS2S.
func IsTrustedS2S(ctx context.Context) bool {
	_, ok := trustedS2SKey.Get(ctx)
	return ok
}

// WithTrustedS2S returns ctx marked as a trusted service-to-service caller.
//
// This is intended for use in tests only. Production code must rely on
// MTLSAuthUnary/MTLSAuthStream's mTLS branch to set the marker after a
// verified client certificate. Setting the marker manually in production
// would let callers bypass RBAC.
func WithTrustedS2S(ctx context.Context) context.Context {
	return trustedS2SKey.Set(ctx, grpcTrustedS2SMarker{})
}

// RequirePermissionUnary returns a unary server interceptor that enforces a
// permission check using the permissions slot populated by AuthUnary (or
// MTLSAuthUnary). Fail-closed semantics mirror
// httpx/middleware/auth.RequirePermission:
//
//   - If [IsTrustedS2S] returns true, the check is bypassed — internal
//     services authenticated via verified mTLS + CN allowlist are trusted
//     explicitly, not by virtue of "happened to have no permissions claim".
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

// MTLSAuthUnary returns a unary server interceptor that accepts two
// authentication modes:
//
//  1. Bearer JWT (same as AuthUnary).
//  2. mTLS client certificate + x-user-id metadata (service-to-service).
//
// For mode 2, the caller's TLS client certificate CN must be in allowedCNs
// and the certificate chain must have been verified by the gRPC TLS layer.
// The middleware reads x-user-id from incoming metadata, validates it as a
// UUID, and stamps a trusted-S2S marker on the context so downstream
// RequirePermissionUnary / RequireScopeUnary interceptors permit the call
// without a permissions claim.
//
// Both provider and allowedCNs are required — the function panics at
// startup if either is nil/empty, matching the HTTP RequireS2SAuth.
//
// An auditor can grep for "MTLSAuthUnary" to find all S2S entry points.
func MTLSAuthUnary(provider *jwtutil.Provider, allowedCNs []string) grpc.UnaryServerInterceptor {
	if provider == nil {
		panic("interceptor: MTLSAuthUnary requires a non-nil JWT provider")
	}
	if len(allowedCNs) == 0 {
		panic("interceptor: MTLSAuthUnary requires at least one allowed CN")
	}
	cnSet := make(map[string]struct{}, len(allowedCNs))
	for _, cn := range allowedCNs {
		cnSet[cn] = struct{}{}
	}
	return func(
		ctx context.Context,
		req any,
		_ *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (any, error) {
		newCtx, err := authenticateMTLSOrJWT(ctx, provider, cnSet)
		if err != nil {
			return nil, err
		}
		return handler(newCtx, req)
	}
}

// MTLSAuthStream returns a stream server interceptor that accepts JWT or
// mTLS+metadata authentication. See [MTLSAuthUnary] for semantics.
func MTLSAuthStream(provider *jwtutil.Provider, allowedCNs []string) grpc.StreamServerInterceptor {
	if provider == nil {
		panic("interceptor: MTLSAuthStream requires a non-nil JWT provider")
	}
	if len(allowedCNs) == 0 {
		panic("interceptor: MTLSAuthStream requires at least one allowed CN")
	}
	cnSet := make(map[string]struct{}, len(allowedCNs))
	for _, cn := range allowedCNs {
		cnSet[cn] = struct{}{}
	}
	return func(
		srv any,
		ss grpc.ServerStream,
		_ *grpc.StreamServerInfo,
		handler grpc.StreamHandler,
	) error {
		newCtx, err := authenticateMTLSOrJWT(ss.Context(), provider, cnSet)
		if err != nil {
			return err
		}
		return handler(srv, &contextStream{ServerStream: ss, ctx: newCtx})
	}
}

// authenticateMTLSOrJWT tries JWT bearer auth first; on absence, falls
// back to verified-mTLS + CN allowlist + x-user-id metadata. The marker is
// stamped only on the mTLS branch, never on the JWT branch.
func authenticateMTLSOrJWT(
	ctx context.Context,
	provider *jwtutil.Provider,
	allowedCNs map[string]struct{},
) (context.Context, error) {
	// Try JWT first.
	if extractBearerToken(ctx) != "" {
		return authenticate(ctx, provider)
	}

	// mTLS branch: require a verified client cert with an allow-listed CN.
	if !verifyClientCertGRPC(ctx, allowedCNs) {
		return ctx, status.Error(codes.Unauthenticated, "unauthorized")
	}

	userID := extractXUserID(ctx)
	if userID == "" || !uuidPattern.MatchString(userID) {
		return ctx, status.Error(codes.Unauthenticated, "unauthorized")
	}

	cn := ""
	if p, ok := peer.FromContext(ctx); ok {
		if tlsInfo, ok := p.AuthInfo.(credentials.TLSInfo); ok {
			if len(tlsInfo.State.PeerCertificates) > 0 {
				cn = tlsInfo.State.PeerCertificates[0].Subject.CommonName
			}
		}
	}
	slog.InfoContext(ctx, "grpc s2s user impersonation",
		slog.String("user_id", userID),
		slog.String("client_cn", cn),
	)

	ctx = userIDKey.Set(ctx, grpcUserID(userID))
	ctx = trustedS2SKey.Set(ctx, grpcTrustedS2SMarker{})
	return ctx, nil
}

// verifyClientCertGRPC checks that the gRPC peer presented a fully verified
// client certificate whose CN is in the allowlist.
//
// The VerifiedChains check is essential: PeerCertificates is populated any
// time a peer presents a certificate, even when chain verification was
// skipped or failed. Trusting PeerCertificates without VerifiedChains lets
// a misconfigured server (ClientAuth=RequestClientCert without ClientCAs)
// admit unverified certs. Only trust an identity that the TLS layer itself
// validated against a trusted CA. Mirrors the HTTP verifyClientCert.
func verifyClientCertGRPC(ctx context.Context, allowedCNs map[string]struct{}) bool {
	p, ok := peer.FromContext(ctx)
	if !ok {
		return false
	}
	tlsInfo, ok := p.AuthInfo.(credentials.TLSInfo)
	if !ok {
		return false
	}
	if len(tlsInfo.State.PeerCertificates) == 0 || len(tlsInfo.State.VerifiedChains) == 0 {
		return false
	}
	cn := tlsInfo.State.PeerCertificates[0].Subject.CommonName
	_, ok = allowedCNs[cn]
	return ok
}

// extractXUserID reads the x-user-id metadata value from the incoming
// context. Returns "" if absent.
func extractXUserID(ctx context.Context) string {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ""
	}
	vals := md.Get(xUserIDMetadataKey)
	if len(vals) == 0 {
		return ""
	}
	return strings.TrimSpace(vals[0])
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
