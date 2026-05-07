package interceptor_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"net"
	"testing"
	"time"

	"github.com/lestrrat-go/jwx/v3/jwa"
	"github.com/lestrrat-go/jwx/v3/jwk"
	"github.com/lestrrat-go/jwx/v3/jwt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"github.com/bds421/rho-kit/grpcx/interceptor"
	"github.com/bds421/rho-kit/security/jwtutil"
)

// testKeyAndProvider creates a jwtutil.Provider with a generated ECDSA key
// and returns the provider plus the private key for signing test tokens.
func testKeyAndProvider(t *testing.T) (*jwtutil.Provider, *ecdsa.PrivateKey) {
	t.Helper()

	privKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	pubJWK, err := jwk.Import(privKey.PublicKey)
	require.NoError(t, err)
	require.NoError(t, pubJWK.Set(jwk.KeyIDKey, "test-key"))
	require.NoError(t, pubJWK.Set(jwk.AlgorithmKey, jwa.ES256()))
	require.NoError(t, pubJWK.Set(jwk.KeyUsageKey, "sig"))

	set := jwk.NewSet()
	require.NoError(t, set.AddKey(pubJWK))

	data, err := json.Marshal(set)
	require.NoError(t, err)

	ks, err := jwtutil.ParseKeySet(data)
	require.NoError(t, err)

	provider := jwtutil.NewProviderWithKeySet(ks)
	return provider, privKey
}

// signTestToken creates a signed JWT with the given subject and permissions.
func signTestToken(t *testing.T, privKey *ecdsa.PrivateKey, subject string, permissions []string) string {
	t.Helper()

	tok, err := jwt.NewBuilder().
		Subject(subject).
		IssuedAt(time.Now()).
		Expiration(time.Now().Add(time.Hour)).
		Build()
	require.NoError(t, err)

	if len(permissions) > 0 {
		require.NoError(t, tok.Set("permissions", permissions))
	}

	jwkKey, err := jwk.Import(privKey)
	require.NoError(t, err)
	require.NoError(t, jwkKey.Set(jwk.KeyIDKey, "test-key"))

	signed, err := jwt.Sign(tok, jwt.WithKey(jwa.ES256(), jwkKey))
	require.NoError(t, err)

	return string(signed)
}

func TestAuthUnary_ValidToken(t *testing.T) {
	provider, privKey := testKeyAndProvider(t)
	token := signTestToken(t, privKey, "user-123", []string{"read", "write"})

	lis := bufconn.Listen(bufSize)
	srv := grpc.NewServer(
		grpc.ChainUnaryInterceptor(
			interceptor.AuthUnary(provider),
		),
	)
	healthpb.RegisterHealthServer(srv, &healthpb.UnimplementedHealthServer{})

	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.GracefulStop)

	conn, err := grpc.NewClient("passthrough:///bufconn",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
			return lis.Dial()
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	ctx := metadata.AppendToOutgoingContext(context.Background(),
		"authorization", "Bearer "+token,
	)

	client := healthpb.NewHealthClient(conn)
	_, err = client.Check(ctx, &healthpb.HealthCheckRequest{})

	// The underlying handler returns Unimplemented, not a real error from auth.
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.Unimplemented, st.Code())
}

func TestAuthUnary_MissingToken(t *testing.T) {
	provider, _ := testKeyAndProvider(t)

	lis := bufconn.Listen(bufSize)
	srv := grpc.NewServer(
		grpc.ChainUnaryInterceptor(
			interceptor.AuthUnary(provider),
		),
	)
	healthpb.RegisterHealthServer(srv, &healthpb.UnimplementedHealthServer{})

	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.GracefulStop)

	conn, err := grpc.NewClient("passthrough:///bufconn",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
			return lis.Dial()
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	client := healthpb.NewHealthClient(conn)
	_, err = client.Check(context.Background(), &healthpb.HealthCheckRequest{})

	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.Unauthenticated, st.Code())
}

func TestAuthUnary_InvalidToken(t *testing.T) {
	provider, _ := testKeyAndProvider(t)

	lis := bufconn.Listen(bufSize)
	srv := grpc.NewServer(
		grpc.ChainUnaryInterceptor(
			interceptor.AuthUnary(provider),
		),
	)
	healthpb.RegisterHealthServer(srv, &healthpb.UnimplementedHealthServer{})

	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.GracefulStop)

	conn, err := grpc.NewClient("passthrough:///bufconn",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
			return lis.Dial()
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	ctx := metadata.AppendToOutgoingContext(context.Background(),
		"authorization", "Bearer invalid-token-here",
	)

	client := healthpb.NewHealthClient(conn)
	_, err = client.Check(ctx, &healthpb.HealthCheckRequest{})

	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.Unauthenticated, st.Code())
}

func TestAuthUnary_SkipMethods(t *testing.T) {
	provider, _ := testKeyAndProvider(t)

	lis := bufconn.Listen(bufSize)
	srv := grpc.NewServer(
		grpc.ChainUnaryInterceptor(
			interceptor.AuthUnary(provider,
				interceptor.WithSkipMethods("/grpc.health.v1.Health/Check"),
			),
		),
	)
	healthpb.RegisterHealthServer(srv, &healthpb.UnimplementedHealthServer{})

	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.GracefulStop)

	conn, err := grpc.NewClient("passthrough:///bufconn",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
			return lis.Dial()
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	// No auth token, but method is skipped.
	client := healthpb.NewHealthClient(conn)
	_, err = client.Check(context.Background(), &healthpb.HealthCheckRequest{})

	// Should reach handler (Unimplemented), not be rejected by auth.
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.Unimplemented, st.Code())
}

func TestAuthUnary_PanicsOnNilProvider(t *testing.T) {
	assert.Panics(t, func() {
		interceptor.AuthUnary(nil)
	})
}

func TestAuthStream_PanicsOnNilProvider(t *testing.T) {
	assert.Panics(t, func() {
		interceptor.AuthStream(nil)
	})
}

func TestAuthStream_MissingToken(t *testing.T) {
	provider, _ := testKeyAndProvider(t)

	lis := bufconn.Listen(bufSize)
	srv := grpc.NewServer(
		grpc.ChainStreamInterceptor(
			interceptor.AuthStream(provider),
		),
	)
	healthpb.RegisterHealthServer(srv, &healthpb.UnimplementedHealthServer{})

	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.GracefulStop)

	conn, err := grpc.NewClient("passthrough:///bufconn",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
			return lis.Dial()
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	client := healthpb.NewHealthClient(conn)
	stream, err := client.Watch(context.Background(), &healthpb.HealthCheckRequest{})
	if err == nil {
		_, err = stream.Recv()
	}

	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.Unauthenticated, st.Code())
}

func TestAuthStream_SkipMethods(t *testing.T) {
	provider, _ := testKeyAndProvider(t)

	lis := bufconn.Listen(bufSize)
	srv := grpc.NewServer(
		grpc.ChainStreamInterceptor(
			interceptor.AuthStream(provider,
				interceptor.WithSkipMethods("/grpc.health.v1.Health/Watch"),
			),
		),
	)
	healthpb.RegisterHealthServer(srv, &healthpb.UnimplementedHealthServer{})

	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.GracefulStop)

	conn, err := grpc.NewClient("passthrough:///bufconn",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
			return lis.Dial()
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	// No auth token, but method is skipped.
	client := healthpb.NewHealthClient(conn)
	stream, err := client.Watch(context.Background(), &healthpb.HealthCheckRequest{})
	if err == nil {
		_, err = stream.Recv()
	}

	// Should reach handler (Unimplemented), not be rejected by auth.
	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.Unimplemented, st.Code())
}

func TestAuthStream_ValidToken(t *testing.T) {
	provider, privKey := testKeyAndProvider(t)
	token := signTestToken(t, privKey, "user-stream-123", []string{"read"})

	lis := bufconn.Listen(bufSize)
	srv := grpc.NewServer(
		grpc.ChainStreamInterceptor(
			interceptor.AuthStream(provider),
		),
	)
	healthpb.RegisterHealthServer(srv, &healthpb.UnimplementedHealthServer{})

	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.GracefulStop)

	conn, err := grpc.NewClient("passthrough:///bufconn",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
			return lis.Dial()
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	ctx := metadata.AppendToOutgoingContext(context.Background(),
		"authorization", "Bearer "+token,
	)

	client := healthpb.NewHealthClient(conn)
	stream, err := client.Watch(ctx, &healthpb.HealthCheckRequest{})
	if err == nil {
		_, err = stream.Recv()
	}

	// Should reach handler (Unimplemented), not be rejected by auth.
	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.Unimplemented, st.Code())
}

// ---------------------------------------------------------------------------
// Authorization primitives (RequirePermission, RequireScope, IsTrustedS2S)
// ---------------------------------------------------------------------------

// noopUnaryInfo is a placeholder UnaryServerInfo for direct interceptor invocation.
var noopUnaryInfo = &grpc.UnaryServerInfo{FullMethod: "/test.Service/Method"}

// noopStreamInfo is a placeholder StreamServerInfo for direct interceptor invocation.
var noopStreamInfo = &grpc.StreamServerInfo{FullMethod: "/test.Service/Method"}

// fakeStream is a minimal grpc.ServerStream that returns a configured ctx.
type fakeStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (f *fakeStream) Context() context.Context { return f.ctx }

func TestRequirePermissionUnary_PanicsOnEmpty(t *testing.T) {
	assert.Panics(t, func() {
		interceptor.RequirePermissionUnary("")
	})
}

func TestRequirePermissionStream_PanicsOnEmpty(t *testing.T) {
	assert.Panics(t, func() {
		interceptor.RequirePermissionStream("")
	})
}

func TestRequireScopeUnary_PanicsOnEmpty(t *testing.T) {
	assert.Panics(t, func() {
		interceptor.RequireScopeUnary("")
	})
}

func TestRequireScopeStream_PanicsOnEmpty(t *testing.T) {
	assert.Panics(t, func() {
		interceptor.RequireScopeStream("")
	})
}

// TestRequirePermissionUnary_NoPermsClaim_PermissionDenied verifies that a
// request with no permissions slot on context AND no trusted-S2S marker is
// rejected with codes.PermissionDenied. This is the fail-closed default.
func TestRequirePermissionUnary_NoPermsClaim_PermissionDenied(t *testing.T) {
	called := false
	handler := func(ctx context.Context, req any) (any, error) {
		called = true
		return "ok", nil
	}

	ic := interceptor.RequirePermissionUnary("read")
	resp, err := ic(context.Background(), nil, noopUnaryInfo, handler)

	require.Error(t, err)
	assert.Nil(t, resp)
	assert.False(t, called, "handler must not run when permission missing")

	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.PermissionDenied, st.Code())
}

// TestRequirePermissionUnary_HasPermission_Allowed verifies that a request
// with the required permission on context is permitted.
func TestRequirePermissionUnary_HasPermission_Allowed(t *testing.T) {
	provider, privKey := testKeyAndProvider(t)
	token := signTestToken(t, privKey, "11111111-1111-1111-1111-111111111111", []string{"read", "write"})

	authCtx, err := authedCtx(t, provider, token)
	require.NoError(t, err)

	called := false
	handler := func(ctx context.Context, req any) (any, error) {
		called = true
		return "ok", nil
	}

	ic := interceptor.RequirePermissionUnary("read")
	resp, err := ic(authCtx, nil, noopUnaryInfo, handler)

	require.NoError(t, err)
	assert.Equal(t, "ok", resp)
	assert.True(t, called)
}

// TestRequirePermissionUnary_TrustedS2S_BypassesCheck verifies that the
// trusted-S2S marker bypasses the permission check even when no permissions
// claim is present. This is the documented S2S composition.
func TestRequirePermissionUnary_TrustedS2S_BypassesCheck(t *testing.T) {
	called := false
	handler := func(ctx context.Context, req any) (any, error) {
		called = true
		return "ok", nil
	}

	// Use the test-only WithTrustedS2S helper to simulate the marker that
	// MTLSAuthUnary's mTLS branch stamps after a verified client cert.
	ctx := interceptor.WithTrustedS2S(context.Background())

	ic := interceptor.RequirePermissionUnary("read")
	resp, err := ic(ctx, nil, noopUnaryInfo, handler)

	require.NoError(t, err)
	assert.Equal(t, "ok", resp)
	assert.True(t, called)
}

// TestRequirePermissionStream_TrustedS2S_BypassesCheck mirrors the unary
// test for streaming RPCs.
func TestRequirePermissionStream_TrustedS2S_BypassesCheck(t *testing.T) {
	called := false
	handler := func(srv any, stream grpc.ServerStream) error {
		called = true
		return nil
	}

	ctx := interceptor.WithTrustedS2S(context.Background())
	ss := &fakeStream{ctx: ctx}

	ic := interceptor.RequirePermissionStream("read")
	err := ic(nil, ss, noopStreamInfo, handler)
	require.NoError(t, err)
	assert.True(t, called)
}

// TestRequirePermissionStream_NoPermsClaim_PermissionDenied verifies the
// fail-closed semantics for the stream variant.
func TestRequirePermissionStream_NoPermsClaim_PermissionDenied(t *testing.T) {
	called := false
	handler := func(srv any, stream grpc.ServerStream) error {
		called = true
		return nil
	}

	ss := &fakeStream{ctx: context.Background()}

	ic := interceptor.RequirePermissionStream("read")
	err := ic(nil, ss, noopStreamInfo, handler)
	require.Error(t, err)
	assert.False(t, called)

	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.PermissionDenied, st.Code())
}

// TestRequireScopeUnary_HasScope_Allowed verifies scope-bearing requests
// pass.
func TestRequireScopeUnary_HasScope_Allowed(t *testing.T) {
	provider, privKey := testKeyAndProvider(t)

	tok, err := jwt.NewBuilder().
		Subject("11111111-1111-1111-1111-111111111111").
		IssuedAt(time.Now()).
		Expiration(time.Now().Add(time.Hour)).
		Build()
	require.NoError(t, err)
	require.NoError(t, tok.Set("scopes", "read write"))

	jwkKey, err := jwk.Import(privKey)
	require.NoError(t, err)
	require.NoError(t, jwkKey.Set(jwk.KeyIDKey, "test-key"))

	signed, err := jwt.Sign(tok, jwt.WithKey(jwa.ES256(), jwkKey))
	require.NoError(t, err)

	authCtx, err := authedCtx(t, provider, string(signed))
	require.NoError(t, err)

	called := false
	handler := func(ctx context.Context, req any) (any, error) {
		called = true
		return "ok", nil
	}

	ic := interceptor.RequireScopeUnary("read")
	resp, err := ic(authCtx, nil, noopUnaryInfo, handler)
	require.NoError(t, err)
	assert.Equal(t, "ok", resp)
	assert.True(t, called)
}

// TestRequireScopeUnary_MissingScope_PermissionDenied verifies fail-closed
// for scope checks: no scopes claim and no trusted-S2S marker → denied.
func TestRequireScopeUnary_MissingScope_PermissionDenied(t *testing.T) {
	handler := func(ctx context.Context, req any) (any, error) {
		return "ok", nil
	}
	ic := interceptor.RequireScopeUnary("admin")
	_, err := ic(context.Background(), nil, noopUnaryInfo, handler)
	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.PermissionDenied, st.Code())
}

// TestIsTrustedS2S_NotSetByJWTAuth verifies the load-bearing invariant:
// the trusted-S2S marker is set ONLY by the mTLS branch of MTLSAuthUnary,
// never by AuthUnary. A handler authenticated via JWT must not be able to
// claim S2S trust.
func TestIsTrustedS2S_NotSetByJWTAuth(t *testing.T) {
	provider, privKey := testKeyAndProvider(t)
	token := signTestToken(t, privKey, "11111111-1111-1111-1111-111111111111", []string{"read"})

	var observedTrusted bool
	var observedUserID string

	// Direct invocation of AuthUnary so we can inspect the resulting context.
	ic := interceptor.AuthUnary(provider)
	_, err := ic(
		metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer "+token)),
		nil,
		noopUnaryInfo,
		func(ctx context.Context, req any) (any, error) {
			observedTrusted = interceptor.IsTrustedS2S(ctx)
			observedUserID = interceptor.UserID(ctx)
			return nil, nil
		},
	)
	require.NoError(t, err)
	assert.False(t, observedTrusted, "JWT auth must NOT set trusted-S2S marker")
	assert.NotEmpty(t, observedUserID, "JWT auth must populate userID")
}

// TestMTLSAuthUnary_PanicsOnNilProvider mirrors HTTP RequireS2SAuth.
func TestMTLSAuthUnary_PanicsOnNilProvider(t *testing.T) {
	assert.Panics(t, func() {
		interceptor.MTLSAuthUnary(nil, interceptor.WithAllowedCNs([]string{"svc"}))
	})
}

// TestMTLSAuthUnary_PanicsOnEmptyIdentities mirrors HTTP RequireS2SAuth.
func TestMTLSAuthUnary_PanicsOnEmptyIdentities(t *testing.T) {
	provider, _ := testKeyAndProvider(t)
	assert.Panics(t, func() {
		interceptor.MTLSAuthUnary(provider)
	})
	assert.Panics(t, func() {
		interceptor.MTLSAuthUnary(provider, interceptor.WithAllowedCNs(nil))
	})
	assert.Panics(t, func() {
		interceptor.MTLSAuthUnary(provider, interceptor.WithAllowedSANs([]string{}))
	})
}

func TestMTLSAuthStream_PanicsOnNilProvider(t *testing.T) {
	assert.Panics(t, func() {
		interceptor.MTLSAuthStream(nil, interceptor.WithAllowedCNs([]string{"svc"}))
	})
}

func TestMTLSAuthStream_PanicsOnEmptyIdentities(t *testing.T) {
	provider, _ := testKeyAndProvider(t)
	assert.Panics(t, func() {
		interceptor.MTLSAuthStream(provider)
	})
}

// TestMTLSAuthUnary_NoCertNoToken_Unauthenticated verifies the mTLS
// fail-closed: a request with neither a Bearer token nor a verified client
// cert is rejected, the same shape as the HTTP RequireS2SAuth.
func TestMTLSAuthUnary_NoCertNoToken_Unauthenticated(t *testing.T) {
	provider, _ := testKeyAndProvider(t)

	ic := interceptor.MTLSAuthUnary(provider, interceptor.WithAllowedCNs([]string{"svc-a"}))

	called := false
	_, err := ic(
		context.Background(),
		nil,
		noopUnaryInfo,
		func(ctx context.Context, req any) (any, error) {
			called = true
			return nil, nil
		},
	)
	require.Error(t, err)
	assert.False(t, called)

	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.Unauthenticated, st.Code())
}

// TestMTLSAuthUnary_JWTPath_NoTrustedMarker verifies that when MTLSAuthUnary
// authenticates via the JWT branch (Bearer header present), it does NOT
// stamp the trusted-S2S marker. Only the mTLS branch should do that.
func TestMTLSAuthUnary_JWTPath_NoTrustedMarker(t *testing.T) {
	provider, privKey := testKeyAndProvider(t)
	token := signTestToken(t, privKey, "11111111-1111-1111-1111-111111111111", []string{"read"})

	ic := interceptor.MTLSAuthUnary(provider, interceptor.WithAllowedCNs([]string{"svc-a"}))

	var observedTrusted bool
	_, err := ic(
		metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer "+token)),
		nil,
		noopUnaryInfo,
		func(ctx context.Context, req any) (any, error) {
			observedTrusted = interceptor.IsTrustedS2S(ctx)
			return nil, nil
		},
	)
	require.NoError(t, err)
	assert.False(t, observedTrusted,
		"JWT branch of MTLSAuthUnary must NOT stamp the trusted-S2S marker")
}

// authedCtx invokes AuthUnary with the given Bearer token and returns the
// authenticated context as observed by a noop handler. Used to set up the
// permissions+scopes slots without mTLS.
func authedCtx(t *testing.T, provider *jwtutil.Provider, token string) (context.Context, error) {
	t.Helper()
	var captured context.Context
	ic := interceptor.AuthUnary(provider)
	_, err := ic(
		metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer "+token)),
		nil,
		noopUnaryInfo,
		func(ctx context.Context, req any) (any, error) {
			captured = ctx
			return nil, nil
		},
	)
	if err != nil {
		return nil, err
	}
	return captured, nil
}
