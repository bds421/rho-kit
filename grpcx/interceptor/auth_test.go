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
