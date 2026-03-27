package interceptor

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/bds421/rho-kit/core/contextutil"
	"github.com/bds421/rho-kit/security/jwtutil"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// Named types for type-safe context keys.
type grpcUserID string
type grpcPermissions []string
type grpcScopes string

var (
	userIDKey      contextutil.Key[grpcUserID]
	permissionsKey contextutil.Key[grpcPermissions]
	scopesKey      contextutil.Key[grpcScopes]
)

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
