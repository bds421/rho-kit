//go:build authtest

package interceptor_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	"github.com/bds421/rho-kit/grpcx/v2/interceptor"
)

// TestRequirePermissionUnary_TrustedS2S_BypassesCheck verifies that the
// trusted-S2S marker bypasses the permission check even when no permissions
// claim is present. This is the documented S2S composition.
//
// Lives under the authtest build tag because WithTrustedS2S is only
// available with that tag — in production builds it panics.
func TestRequirePermissionUnary_TrustedS2S_BypassesCheck(t *testing.T) {
	called := false
	handler := func(ctx context.Context, req any) (any, error) {
		called = true
		return "ok", nil
	}

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
