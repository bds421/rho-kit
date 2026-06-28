package interceptor_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	"github.com/bds421/rho-kit/grpcx/v2/interceptor"
	"github.com/bds421/rho-kit/security/v2/jwtutil"
)

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