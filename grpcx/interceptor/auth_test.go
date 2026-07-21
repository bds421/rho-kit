package interceptor_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/lestrrat-go/jwx/v3/jwa"
	"github.com/lestrrat-go/jwx/v3/jwk"
	"github.com/lestrrat-go/jwx/v3/jwt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"github.com/bds421/rho-kit/grpcx/v2/interceptor"
	"github.com/bds421/rho-kit/security/v2/jwtutil"
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

	provider := jwtutil.NewProviderWithKeySet(ks,
		jwtutil.WithAllowAnyIssuer(),
		jwtutil.WithAllowAnyAudience(),
	)
	return provider, privKey
}

func allowS2SImpersonationForTest() interceptor.MTLSIdentityOption {
	return interceptor.WithS2SImpersonationGuard(func(context.Context, string, string) error {
		return nil
	})
}

func TestWithSkipMethodsClonesInput(t *testing.T) {
	provider, _ := testKeyAndProvider(t)
	methods := []string{"/rho.test.Service/Check"}
	opt := interceptor.WithSkipMethods(methods...)
	methods[0] = "/rho.test.Service/Mutated"

	unary := interceptor.AuthUnary(provider, opt)
	called := false
	resp, err := unary(context.Background(), nil, &grpc.UnaryServerInfo{FullMethod: "/rho.test.Service/Check"}, func(context.Context, any) (any, error) {
		called = true
		return "ok", nil
	})

	require.NoError(t, err)
	assert.True(t, called)
	assert.Equal(t, "ok", resp)
}

func TestWithMTLSSkipMethodsClonesInput(t *testing.T) {
	provider, _ := testKeyAndProvider(t)
	methods := []string{"/rho.test.Service/Check"}
	opt := interceptor.WithMTLSSkipMethods(methods...)
	methods[0] = "/rho.test.Service/Mutated"

	unary := interceptor.MTLSAuthUnary(provider,
		interceptor.WithAllowedCNs("rho-service"),
		allowS2SImpersonationForTest(),
		opt,
	)
	called := false
	resp, err := unary(context.Background(), nil, &grpc.UnaryServerInfo{FullMethod: "/rho.test.Service/Check"}, func(context.Context, any) (any, error) {
		called = true
		return "ok", nil
	})

	require.NoError(t, err)
	assert.True(t, called)
	assert.Equal(t, "ok", resp)
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
	token := signTestToken(t, privKey, "11111111-1111-1111-1111-111111111111", []string{"read", "write"})

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

func TestAuthUnary_RejectsDuplicateAuthorizationMetadata(t *testing.T) {
	provider, privKey := testKeyAndProvider(t)
	token := signTestToken(t, privKey, "11111111-1111-1111-1111-111111111111", []string{"read"})

	called := false
	ic := interceptor.AuthUnary(provider)
	_, err := ic(
		metadata.NewIncomingContext(context.Background(), metadata.Pairs(
			"authorization", "Bearer "+token,
			"authorization", "Bearer invalid-token",
		)),
		nil,
		noopUnaryInfo,
		func(context.Context, any) (any, error) {
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

func TestAuthUnary_RejectsAmbiguousAuthorizationMetadata(t *testing.T) {
	provider, privKey := testKeyAndProvider(t)
	token := signTestToken(t, privKey, "11111111-1111-1111-1111-111111111111", []string{"read"})

	tests := map[string]string{
		"edge whitespace": " Bearer " + token,
		"trailing token":  "Bearer " + token + " ",
		"empty token":     "Bearer ",
		"control":         "Bearer " + token + "\n",
		"embedded control": "Bearer " + token[:len(token)/2] +
			"\x00" + token[len(token)/2:],
		"comma combined": "Bearer " + token + ",Bearer invalid-token",
		"oversized":      "Bearer " + strings.Repeat("a", 8193),
	}
	for name, value := range tests {
		t.Run(name, func(t *testing.T) {
			called := false
			ic := interceptor.AuthUnary(provider)
			_, err := ic(
				metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", value)),
				nil,
				noopUnaryInfo,
				func(context.Context, any) (any, error) {
					called = true
					return nil, nil
				},
			)
			require.Error(t, err)
			assert.False(t, called)
			st, ok := status.FromError(err)
			require.True(t, ok)
			assert.Equal(t, codes.Unauthenticated, st.Code())
		})
	}
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

func TestAuthUnary_PanicsOnNilOption(t *testing.T) {
	provider, _ := testKeyAndProvider(t)
	assert.Panics(t, func() {
		interceptor.AuthUnary(provider, nil)
	})
}

func TestAuthStream_PanicsOnNilProvider(t *testing.T) {
	assert.Panics(t, func() {
		interceptor.AuthStream(nil)
	})
}

func TestAuthStream_PanicsOnNilOption(t *testing.T) {
	provider, _ := testKeyAndProvider(t)
	assert.Panics(t, func() {
		interceptor.AuthStream(provider, nil)
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
	token := signTestToken(t, privKey, "22222222-2222-2222-2222-222222222222", []string{"read"})

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

func mtlsIncomingContext(cert *x509.Certificate) context.Context {
	return mtlsIncomingContextWithMetadata(cert, metadata.Pairs(
		"x-user-id", "11111111-1111-1111-1111-111111111111",
	))
}

func mtlsIncomingContextWithMetadata(cert *x509.Certificate, md metadata.MD) context.Context {
	ctx := metadata.NewIncomingContext(context.Background(), md)
	state := tls.ConnectionState{
		PeerCertificates: []*x509.Certificate{cert},
		VerifiedChains:   [][]*x509.Certificate{{cert}},
	}
	return peer.NewContext(ctx, &peer.Peer{AuthInfo: credentials.TLSInfo{State: state}})
}

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
		interceptor.MTLSAuthUnary(nil, interceptor.WithAllowedCNs("svc"))
	})
}

func TestMTLSAuthUnary_PanicsOnNilOption(t *testing.T) {
	provider, _ := testKeyAndProvider(t)
	assert.Panics(t, func() {
		interceptor.MTLSAuthUnary(provider, nil)
	})
}

// TestMTLSAuthUnary_PanicsOnEmptyIdentities mirrors HTTP RequireS2SAuth.
func TestMTLSAuthUnary_PanicsOnEmptyIdentities(t *testing.T) {
	provider, _ := testKeyAndProvider(t)
	assert.Panics(t, func() {
		interceptor.MTLSAuthUnary(provider, allowS2SImpersonationForTest())
	})
	assert.Panics(t, func() {
		interceptor.MTLSAuthUnary(provider, allowS2SImpersonationForTest(), interceptor.WithAllowedCNs())
	})
	assert.Panics(t, func() {
		interceptor.MTLSAuthUnary(provider, allowS2SImpersonationForTest(), interceptor.WithAllowedSANs())
	})
}

func TestMTLSAuthUnary_PanicsWithoutImpersonationGuard(t *testing.T) {
	provider, _ := testKeyAndProvider(t)
	assert.Panics(t, func() {
		interceptor.MTLSAuthUnary(provider, interceptor.WithAllowedCNs("svc"))
	})
}

func TestMTLSAuthStream_PanicsOnNilProvider(t *testing.T) {
	assert.Panics(t, func() {
		interceptor.MTLSAuthStream(nil, interceptor.WithAllowedCNs("svc"))
	})
}

func TestMTLSAuthStream_PanicsOnNilOption(t *testing.T) {
	provider, _ := testKeyAndProvider(t)
	assert.Panics(t, func() {
		interceptor.MTLSAuthStream(provider, nil)
	})
}

func TestMTLSAuthStream_PanicsOnEmptyIdentities(t *testing.T) {
	provider, _ := testKeyAndProvider(t)
	assert.Panics(t, func() {
		interceptor.MTLSAuthStream(provider, allowS2SImpersonationForTest())
	})
}

func TestMTLSAuthStream_PanicsWithoutImpersonationGuard(t *testing.T) {
	provider, _ := testKeyAndProvider(t)
	assert.Panics(t, func() {
		interceptor.MTLSAuthStream(provider, interceptor.WithAllowedCNs("svc"))
	})
}

func TestWithS2SImpersonationGuard_PanicsOnNil(t *testing.T) {
	assert.Panics(t, func() {
		interceptor.WithS2SImpersonationGuard(nil)
	})
}

// TestMTLSAuthUnary_NoCertNoToken_Unauthenticated verifies the mTLS
// fail-closed: a request with neither a Bearer token nor a verified client
// cert is rejected, the same shape as the HTTP RequireS2SAuth.
func TestMTLSAuthUnary_NoCertNoToken_Unauthenticated(t *testing.T) {
	provider, _ := testKeyAndProvider(t)

	ic := interceptor.MTLSAuthUnary(provider, allowS2SImpersonationForTest(), interceptor.WithAllowedCNs("svc-a"))

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

func TestMTLSAuthUnary_GuardPanicRejects(t *testing.T) {
	provider, _ := testKeyAndProvider(t)
	ic := interceptor.MTLSAuthUnary(provider,
		interceptor.WithAllowedCNs("svc-a"),
		interceptor.WithS2SImpersonationGuard(func(context.Context, string, string) error {
			panic("guard failed")
		}),
	)
	cert := &x509.Certificate{
		Subject:     pkix.Name{CommonName: "svc-a"},
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}

	called := false
	_, err := ic(
		mtlsIncomingContext(cert),
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
	assert.Equal(t, codes.PermissionDenied, st.Code())
}

func TestMTLSAuthUnary_GuardErrorRejectsAndReceivesIdentity(t *testing.T) {
	provider, _ := testKeyAndProvider(t)
	guardCalled := false
	ic := interceptor.MTLSAuthUnary(provider,
		interceptor.WithAllowedCNs("svc-a"),
		interceptor.WithS2SImpersonationGuard(func(_ context.Context, identity, userID string) error {
			guardCalled = true
			assert.Equal(t, "cn:svc-a", identity)
			assert.Equal(t, "11111111-1111-1111-1111-111111111111", userID)
			return errors.New("not allowed")
		}),
	)
	cert := &x509.Certificate{
		Subject:     pkix.Name{CommonName: "svc-a"},
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}

	called := false
	_, err := ic(
		mtlsIncomingContext(cert),
		nil,
		noopUnaryInfo,
		func(ctx context.Context, req any) (any, error) {
			called = true
			return nil, nil
		},
	)
	require.Error(t, err)
	assert.True(t, guardCalled)
	assert.False(t, called)
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.PermissionDenied, st.Code())
}

func TestMTLSAuthUnary_RejectsMalformedAuthorizationBeforeMTLSFallback(t *testing.T) {
	provider, _ := testKeyAndProvider(t)
	ic := interceptor.MTLSAuthUnary(provider, allowS2SImpersonationForTest(), interceptor.WithAllowedCNs("svc-a"))
	cert := &x509.Certificate{
		Subject:     pkix.Name{CommonName: "svc-a"},
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}

	called := false
	_, err := ic(
		mtlsIncomingContextWithMetadata(cert, metadata.Pairs(
			"authorization", "Basic dXNlcjpwYXNz",
			"x-user-id", "11111111-1111-1111-1111-111111111111",
		)),
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

func TestMTLSAuthUnary_RejectsDuplicateAuthorizationBeforeMTLSFallback(t *testing.T) {
	provider, privKey := testKeyAndProvider(t)
	token := signTestToken(t, privKey, "11111111-1111-1111-1111-111111111111", []string{"read"})
	ic := interceptor.MTLSAuthUnary(provider, allowS2SImpersonationForTest(), interceptor.WithAllowedCNs("svc-a"))
	cert := &x509.Certificate{
		Subject:     pkix.Name{CommonName: "svc-a"},
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}

	called := false
	_, err := ic(
		mtlsIncomingContextWithMetadata(cert, metadata.Pairs(
			"authorization", "Bearer "+token,
			"authorization", "Bearer invalid-token",
			"x-user-id", "11111111-1111-1111-1111-111111111111",
		)),
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

func TestMTLSAuthUnary_RejectsDuplicateXUserIDMetadata(t *testing.T) {
	provider, _ := testKeyAndProvider(t)
	ic := interceptor.MTLSAuthUnary(provider, allowS2SImpersonationForTest(), interceptor.WithAllowedCNs("svc-a"))
	cert := &x509.Certificate{
		Subject:     pkix.Name{CommonName: "svc-a"},
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}

	called := false
	_, err := ic(
		mtlsIncomingContextWithMetadata(cert, metadata.Pairs(
			"x-user-id", "11111111-1111-1111-1111-111111111111",
			"x-user-id", "22222222-2222-2222-2222-222222222222",
		)),
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

func TestMTLSAuthUnary_RejectsAmbiguousXUserIDMetadata(t *testing.T) {
	provider, _ := testKeyAndProvider(t)
	ic := interceptor.MTLSAuthUnary(provider, allowS2SImpersonationForTest(), interceptor.WithAllowedCNs("svc-a"))
	cert := &x509.Certificate{
		Subject:     pkix.Name{CommonName: "svc-a"},
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}

	tests := []struct {
		name  string
		value string
	}{
		{name: "edge whitespace", value: " 11111111-1111-1111-1111-111111111111 "},
		{name: "internal whitespace", value: "11111111-1111-1111-1111-11111111 1111"},
		{name: "comma combined", value: "11111111-1111-1111-1111-111111111111,22222222-2222-2222-2222-222222222222"},
		{name: "control", value: "11111111-1111-1111-1111-111111111111\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			called := false
			_, err := ic(
				mtlsIncomingContextWithMetadata(cert, metadata.Pairs("x-user-id", tt.value)),
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
		})
	}
}

func TestMTLSAuthUnary_RejectsCACert(t *testing.T) {
	provider, _ := testKeyAndProvider(t)
	ic := interceptor.MTLSAuthUnary(provider, allowS2SImpersonationForTest(), interceptor.WithAllowedCNs("svc-a"))
	cert := &x509.Certificate{
		Subject:     pkix.Name{CommonName: "svc-a"},
		IsCA:        true,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}

	called := false
	_, err := ic(
		mtlsIncomingContext(cert),
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

func TestMTLSAuthUnary_RejectsServerOnlyCert(t *testing.T) {
	provider, _ := testKeyAndProvider(t)
	ic := interceptor.MTLSAuthUnary(provider, allowS2SImpersonationForTest(), interceptor.WithAllowedCNs("svc-a"))
	cert := &x509.Certificate{
		Subject:     pkix.Name{CommonName: "svc-a"},
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}

	called := false
	_, err := ic(
		mtlsIncomingContext(cert),
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

	ic := interceptor.MTLSAuthUnary(provider, allowS2SImpersonationForTest(), interceptor.WithAllowedCNs("svc-a"))

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

// TestMTLSAuthUnary_MTLSPath_Success drives the full mTLS branch of
// MTLSAuthUnary to success — the feature's core contract that every other
// MTLSAuth test only ever exercises on a rejection path: a verified client
// cert with an allow-listed identity plus a valid x-user-id is admitted, the
// handler runs, IsTrustedS2S(ctx) is true, and UserID(ctx) equals the
// impersonated UUID.
func TestMTLSAuthUnary_MTLSPath_Success(t *testing.T) {
	provider, _ := testKeyAndProvider(t)

	const wantUserID = "11111111-1111-1111-1111-111111111111"
	var guardIdentity, guardUserID string
	ic := interceptor.MTLSAuthUnary(provider,
		interceptor.WithAllowedCNs("svc-a"),
		interceptor.WithS2SImpersonationGuard(func(_ context.Context, identity, userID string) error {
			guardIdentity = identity
			guardUserID = userID
			return nil
		}),
	)
	cert := &x509.Certificate{
		Subject:     pkix.Name{CommonName: "svc-a"},
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}

	called := false
	var observedTrusted bool
	var observedUserID string
	resp, err := ic(
		mtlsIncomingContext(cert),
		nil,
		noopUnaryInfo,
		func(ctx context.Context, _ any) (any, error) {
			called = true
			observedTrusted = interceptor.IsTrustedS2S(ctx)
			observedUserID = interceptor.UserID(ctx)
			return "ok", nil
		},
	)
	require.NoError(t, err)
	assert.True(t, called, "handler must run on a successful mTLS S2S admission")
	assert.Equal(t, "ok", resp)
	assert.True(t, observedTrusted, "mTLS branch must stamp the trusted-S2S marker")
	assert.Equal(t, wantUserID, observedUserID, "UserID must equal the impersonated UUID")
	assert.Equal(t, "cn:svc-a", guardIdentity, "guard receives the matched client identity")
	assert.Equal(t, wantUserID, guardUserID, "guard receives the impersonated UUID")
}

// TestMTLSAuthStream_MTLSPath_Success mirrors the unary success path for the
// streaming variant, asserting the contextStream wrapping so the handler's
// stream observes the authenticated context (trusted marker + user ID).
func TestMTLSAuthStream_MTLSPath_Success(t *testing.T) {
	provider, _ := testKeyAndProvider(t)

	const wantUserID = "11111111-1111-1111-1111-111111111111"
	ic := interceptor.MTLSAuthStream(provider,
		interceptor.WithAllowedCNs("svc-a"),
		allowS2SImpersonationForTest(),
	)
	cert := &x509.Certificate{
		Subject:     pkix.Name{CommonName: "svc-a"},
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}

	called := false
	var observedTrusted bool
	var observedUserID string
	ss := &fakeStream{ctx: mtlsIncomingContext(cert)}
	err := ic(nil, ss, noopStreamInfo, func(_ any, stream grpc.ServerStream) error {
		called = true
		ctx := stream.Context()
		observedTrusted = interceptor.IsTrustedS2S(ctx)
		observedUserID = interceptor.UserID(ctx)
		return nil
	})
	require.NoError(t, err)
	assert.True(t, called, "handler must run on a successful mTLS S2S admission")
	assert.True(t, observedTrusted,
		"contextStream must carry the trusted-S2S marker into the handler")
	assert.Equal(t, wantUserID, observedUserID,
		"contextStream must carry the impersonated UUID into the handler")
}

// TestMTLSAuthUnary_MTLSPath_TrustedS2SBypassesPermissionCheck composes the
// real mTLS branch with RequirePermissionUnary to verify, in the DEFAULT
// build (no authtest tag), that an mTLS-admitted caller with no permissions
// claim is DENIED by default (no permission laundering) and only granted
// when WithTrustedS2SBypass is opted in. This exercises the production
// path that stamps the marker rather than the authtest-only WithTrustedS2S
// shortcut.
func TestMTLSAuthUnary_MTLSPath_TrustedS2SDoesNotBypassByDefault(t *testing.T) {
	provider, _ := testKeyAndProvider(t)

	auth := interceptor.MTLSAuthUnary(provider,
		interceptor.WithAllowedCNs("svc-a"),
		allowS2SImpersonationForTest(),
	)
	requirePerm := interceptor.RequirePermissionUnary("admin")
	cert := &x509.Certificate{
		Subject:     pkix.Name{CommonName: "svc-a"},
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}

	called := false
	handler := func(context.Context, any) (any, error) {
		called = true
		return "ok", nil
	}
	// auth (outer) -> requirePerm (inner) mirrors the kit chain ordering.
	_, err := auth(
		mtlsIncomingContext(cert),
		nil,
		noopUnaryInfo,
		func(ctx context.Context, req any) (any, error) {
			return requirePerm(ctx, req, noopUnaryInfo, handler)
		},
	)
	require.Error(t, err)
	assert.False(t, called,
		"default RequirePermission must not pass on trusted-S2S alone (permission laundering)")
}

func TestMTLSAuthUnary_MTLSPath_TrustedS2SBypassOptIn(t *testing.T) {
	provider, _ := testKeyAndProvider(t)

	auth := interceptor.MTLSAuthUnary(provider,
		interceptor.WithAllowedCNs("svc-a"),
		allowS2SImpersonationForTest(),
	)
	requirePerm := interceptor.RequirePermissionUnary("admin", interceptor.WithTrustedS2SBypass())
	cert := &x509.Certificate{
		Subject:     pkix.Name{CommonName: "svc-a"},
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}

	called := false
	handler := func(context.Context, any) (any, error) {
		called = true
		return "ok", nil
	}
	resp, err := auth(
		mtlsIncomingContext(cert),
		nil,
		noopUnaryInfo,
		func(ctx context.Context, req any) (any, error) {
			return requirePerm(ctx, req, noopUnaryInfo, handler)
		},
	)
	require.NoError(t, err)
	assert.Equal(t, "ok", resp)
	assert.True(t, called)
}

func TestUserPermissions_ReturnsClone(t *testing.T) {
	provider, privKey := testKeyAndProvider(t)
	token := signTestToken(t, privKey, "11111111-1111-1111-1111-111111111111", []string{"read", "write"})
	ctx, err := authedCtx(t, provider, token)
	require.NoError(t, err)

	perms := interceptor.UserPermissions(ctx)
	require.Equal(t, []string{"read", "write"}, perms)

	perms[0] = "admin"
	assert.Equal(t, []string{"read", "write"}, interceptor.UserPermissions(ctx))
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
