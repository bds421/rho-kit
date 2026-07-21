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

// TestRequirePermissionUnary_TrustedS2S_DefaultDeniesWithoutPerms pins the
// safer default: the trusted-S2S marker alone does NOT satisfy
// RequirePermission when no permissions claim is present. Opt in with
// WithTrustedS2SBypass for service-level trust.
//
// Lives under the authtest build tag because WithTrustedS2S is only
// available with that tag — in production builds it panics.
func TestRequirePermissionUnary_TrustedS2S_DefaultDeniesWithoutPerms(t *testing.T) {
	called := false
	handler := func(ctx context.Context, req any) (any, error) {
		called = true
		return "ok", nil
	}

	ctx := interceptor.WithTrustedS2S(context.Background())

	ic := interceptor.RequirePermissionUnary("read")
	_, err := ic(ctx, nil, noopUnaryInfo, handler)

	require.Error(t, err)
	assert.False(t, called, "default must not launder permissions via trusted-S2S alone")
}

// TestRequirePermissionUnary_TrustedS2S_BypassOptIn verifies that
// WithTrustedS2SBypass restores the historical short-circuit for RPCs that
// intentionally treat allow-listed mTLS as full authority.
func TestRequirePermissionUnary_TrustedS2S_BypassOptIn(t *testing.T) {
	called := false
	handler := func(ctx context.Context, req any) (any, error) {
		called = true
		return "ok", nil
	}

	ctx := interceptor.WithTrustedS2S(context.Background())

	ic := interceptor.RequirePermissionUnary("read", interceptor.WithTrustedS2SBypass())
	resp, err := ic(ctx, nil, noopUnaryInfo, handler)

	require.NoError(t, err)
	assert.Equal(t, "ok", resp)
	assert.True(t, called)
}

// TestRequirePermissionStream_TrustedS2S_BypassOptIn mirrors the unary
// opt-in path for streaming RPCs.
func TestRequirePermissionStream_TrustedS2S_BypassOptIn(t *testing.T) {
	called := false
	handler := func(srv any, stream grpc.ServerStream) error {
		called = true
		return nil
	}

	ctx := interceptor.WithTrustedS2S(context.Background())
	ss := &fakeStream{ctx: ctx}

	ic := interceptor.RequirePermissionStream("read", interceptor.WithTrustedS2SBypass())
	err := ic(nil, ss, noopStreamInfo, handler)
	require.NoError(t, err)
	assert.True(t, called)
}
