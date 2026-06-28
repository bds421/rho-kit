package interceptor_test

import (
	"context"
	"crypto/ecdsa"
	"testing"
	"time"

	"github.com/lestrrat-go/jwx/v3/jwa"
	"github.com/lestrrat-go/jwx/v3/jwk"
	"github.com/lestrrat-go/jwx/v3/jwt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	"github.com/bds421/rho-kit/grpcx/v2/interceptor"
	"github.com/bds421/rho-kit/security/v2/jwtutil"
)

func signTestTokenWithClaims(t *testing.T, privKey *ecdsa.PrivateKey, claims map[string]any) string {
	t.Helper()
	tok, err := jwt.NewBuilder().
		IssuedAt(time.Now()).
		Expiration(time.Now().Add(time.Hour)).
		Build()
	require.NoError(t, err)
	for k, v := range claims {
		require.NoError(t, tok.Set(k, v))
	}
	jwkKey, err := jwk.Import(privKey)
	require.NoError(t, err)
	require.NoError(t, jwkKey.Set(jwk.KeyIDKey, "test-key"))
	signed, err := jwt.Sign(tok, jwt.WithKey(jwa.ES256(), jwkKey))
	require.NoError(t, err)
	return string(signed)
}

func TestGRPCIdentity_JWTStampsSubjectAndActor(t *testing.T) {
	provider, privKey := testKeyAndProvider(t)
	subject := "11111111-1111-1111-1111-111111111111"
	token := signTestToken(t, privKey, subject, nil)

	unary := interceptor.AuthUnary(provider)
	var gotSubj, gotActor string
	var gotKind interceptor.ActorKind
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer "+token))
	_, err := unary(ctx, nil, &grpc.UnaryServerInfo{FullMethod: "/test.Service/Run"}, func(ctx context.Context, _ any) (any, error) {
		gotSubj = interceptor.Subject(ctx)
		gotActor = interceptor.Actor(ctx)
		gotKind = interceptor.ActorKindFromContext(ctx)
		return nil, nil
	})
	require.NoError(t, err)
	assert.Equal(t, subject, gotSubj)
	assert.Equal(t, subject, gotActor)
	assert.Equal(t, interceptor.ActorUser, gotKind)
}

func TestGRPCIdentity_NormalizePrefixedSubject(t *testing.T) {
	provider, privKey := testKeyAndProvider(t)
	uuid := "22222222-2222-2222-2222-222222222222"
	token := signTestToken(t, privKey, jwtutil.SubjectPrefixUser+uuid, nil)

	unary := interceptor.AuthUnary(provider)
	var gotSubj string
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer "+token))
	_, err := unary(ctx, nil, &grpc.UnaryServerInfo{FullMethod: "/test.Service/Run"}, func(ctx context.Context, _ any) (any, error) {
		gotSubj = interceptor.Subject(ctx)
		return nil, nil
	})
	require.NoError(t, err)
	assert.Equal(t, uuid, gotSubj)
}

func TestGRPCIdentity_ServiceJWTFromClientID(t *testing.T) {
	provider, privKey := testKeyAndProvider(t)
	subject := "33333333-3333-3333-3333-333333333333"
	token := signTestTokenWithClaims(t, privKey, map[string]any{
		"sub":       subject,
		"client_id": "billing-svc",
	})

	unary := interceptor.AuthUnary(provider, interceptor.AsAuthOption(
		interceptor.WithJWTServiceActorFromClaim("client_id"),
	))
	var gotActor string
	var gotKind interceptor.ActorKind
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer "+token))
	_, err := unary(ctx, nil, &grpc.UnaryServerInfo{FullMethod: "/test.Service/Run"}, func(ctx context.Context, _ any) (any, error) {
		gotActor = interceptor.Actor(ctx)
		gotKind = interceptor.ActorKindFromContext(ctx)
		return nil, nil
	})
	require.NoError(t, err)
	assert.Equal(t, "billing-svc", gotActor)
	assert.Equal(t, interceptor.ActorService, gotKind)
}