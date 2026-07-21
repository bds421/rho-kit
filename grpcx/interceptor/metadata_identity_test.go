package interceptor_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	clientinterceptor "github.com/bds421/rho-kit/grpcx/v2/client/interceptor"
	"github.com/bds421/rho-kit/grpcx/v2/interceptor"
)

func TestAppendOutgoingIdentity(t *testing.T) {
	subject := "11111111-1111-1111-1111-111111111111"
	ctx := context.Background()
	ctx = metadata.NewOutgoingContext(ctx, metadata.Pairs("x-custom", "keep"))
	ctx = stampTestIdentity(t, ctx, subject, "payments-svc", interceptor.ActorService)

	out := interceptor.AppendOutgoingIdentity(ctx)
	md, ok := metadata.FromOutgoingContext(out)
	require.True(t, ok)
	assert.Equal(t, []string{subject}, md.Get(interceptor.MetadataSubjectKey))
	assert.Equal(t, []string{subject}, md.Get(interceptor.MetadataLegacyUserKey))
	assert.Equal(t, []string{"payments-svc"}, md.Get(interceptor.MetadataActorKey))
	assert.Equal(t, []string{"service"}, md.Get(interceptor.MetadataActorKindKey))
	assert.Equal(t, []string{"keep"}, md.Get("x-custom"))
}

// TestAppendOutgoingIdentity_PreservesCallerUserID pins the contract that
// a pre-existing x-user-id is never overwritten when subject is also set.
func TestAppendOutgoingIdentity_PreservesCallerUserID(t *testing.T) {
	subject := "11111111-1111-1111-1111-111111111111"
	ctx := stampTestIdentity(t, context.Background(), subject, "payments-svc", interceptor.ActorService)
	ctx = metadata.NewOutgoingContext(ctx, metadata.Pairs(interceptor.MetadataLegacyUserKey, "caller-set-user"))

	out := interceptor.AppendOutgoingIdentity(ctx)
	md, ok := metadata.FromOutgoingContext(out)
	require.True(t, ok)
	assert.Equal(t, []string{"caller-set-user"}, md.Get(interceptor.MetadataLegacyUserKey))
	assert.Equal(t, []string{subject}, md.Get(interceptor.MetadataSubjectKey))
}

func TestClientPropagation_InjectsIdentityMetadata(t *testing.T) {
	subject := "11111111-1111-1111-1111-111111111111"
	ctx := stampTestIdentity(t, context.Background(), subject, "payments-svc", interceptor.ActorService)

	icpt := clientinterceptor.PropagationUnaryClientInterceptor()
	var seen metadata.MD
	err := icpt(ctx, "/svc/Method", nil, nil, nil,
		func(ctx context.Context, _ string, _, _ any, _ *grpc.ClientConn, _ ...grpc.CallOption) error {
			seen, _ = metadata.FromOutgoingContext(ctx)
			return nil
		},
	)
	require.NoError(t, err)
	assert.Equal(t, []string{subject}, seen.Get(interceptor.MetadataSubjectKey))
	assert.Equal(t, []string{subject}, seen.Get(interceptor.MetadataLegacyUserKey))
	assert.Equal(t, []string{"payments-svc"}, seen.Get(interceptor.MetadataActorKey))
	assert.Equal(t, []string{"service"}, seen.Get(interceptor.MetadataActorKindKey))
}

func stampTestIdentity(t *testing.T, ctx context.Context, subject, actor string, kind interceptor.ActorKind) context.Context {
	t.Helper()
	provider, privKey := testKeyAndProvider(t)
	_ = privKey
	unary := interceptor.AuthUnary(provider, interceptor.AsAuthOption(
		interceptor.WithJWTServiceActorFromClaim("client_id"),
	))
	token := signTestTokenWithClaims(t, privKey, map[string]any{
		"sub": subject, "client_id": actor,
	})
	ctx = metadata.NewIncomingContext(ctx, metadata.Pairs("authorization", "Bearer "+token))
	var out context.Context
	_, err := unary(ctx, nil, &grpc.UnaryServerInfo{FullMethod: "/test.Service/Run"}, func(c context.Context, _ any) (any, error) {
		out = c
		return nil, nil
	})
	require.NoError(t, err)
	return out
}