package interceptor_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"

	"github.com/bds421/rho-kit/grpcx/v2/interceptor"
)

// TestAppendOutgoingIdentity_PropagatesPermissionsAndScopes pins the
// entitlement metadata keys used for trusted-S2S hops.
func TestAppendOutgoingIdentity_PropagatesPermissionsAndScopes(t *testing.T) {
	subject := "11111111-1111-1111-1111-111111111111"
	provider, privKey := testKeyAndProvider(t)
	token := signTestTokenWithClaims(t, privKey, map[string]any{
		"sub":         subject,
		"permissions": []string{"orders:read", "orders:write"},
		"scopes":      "api:read api:write",
	})
	unary := interceptor.AuthUnary(provider)
	var authed context.Context
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer "+token))
	_, err := unary(ctx, nil, &grpc.UnaryServerInfo{FullMethod: "/test.Service/Run"}, func(c context.Context, _ any) (any, error) {
		authed = c
		return nil, nil
	})
	require.NoError(t, err)

	out := interceptor.AppendOutgoingIdentity(authed)
	md, ok := metadata.FromOutgoingContext(out)
	require.True(t, ok)
	assert.Equal(t, []string{"orders:read", "orders:write"}, md.Get(interceptor.MetadataPermissionsKey))
	assert.Equal(t, []string{"api:read", "api:write"}, md.Get(interceptor.MetadataScopesKey))
	assert.Equal(t, []string{subject}, md.Get(interceptor.MetadataSubjectKey))
}

// TestCrossHop_RequirePermission_UsesPropagatedEntitlements exercises the
// full path: JWT auth stamps perms → AppendOutgoingIdentity → mTLS peer
// with metadata → MTLSAuth adopts perms → RequirePermission enforces them
// without WithTrustedS2SBypass.
func TestCrossHop_RequirePermission_UsesPropagatedEntitlements(t *testing.T) {
	subject := "11111111-1111-1111-1111-111111111111"
	provider, privKey := testKeyAndProvider(t)
	token := signTestTokenWithClaims(t, privKey, map[string]any{
		"sub":         subject,
		"permissions": []string{"orders:write"},
		"scopes":      "api:write",
	})
	unary := interceptor.AuthUnary(provider)
	var hop1 context.Context
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer "+token))
	_, err := unary(ctx, nil, &grpc.UnaryServerInfo{FullMethod: "/svc.A/Handle"}, func(c context.Context, _ any) (any, error) {
		hop1 = c
		return nil, nil
	})
	require.NoError(t, err)

	// Service A dials B: stamp identity + entitlements onto outgoing metadata,
	// then present as inbound mTLS on B.
	outgoing := interceptor.AppendOutgoingIdentity(hop1)
	outMD, ok := metadata.FromOutgoingContext(outgoing)
	require.True(t, ok)

	cert := &x509.Certificate{
		Subject:     pkix.Name{CommonName: "svc-a"},
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	inMD := outMD.Copy()
	// Ensure x-user-id is present for mTLS impersonation (AppendOutgoing sets it).
	require.Equal(t, []string{subject}, inMD.Get(interceptor.MetadataLegacyUserKey))

	mtlsCtx := metadata.NewIncomingContext(context.Background(), inMD)
	state := tls.ConnectionState{
		PeerCertificates: []*x509.Certificate{cert},
		VerifiedChains:   [][]*x509.Certificate{{cert}},
	}
	mtlsCtx = peer.NewContext(mtlsCtx, &peer.Peer{AuthInfo: credentials.TLSInfo{State: state}})

	authB := interceptor.MTLSAuthUnary(provider,
		interceptor.WithAllowedCNs("svc-a"),
		interceptor.WithS2SImpersonationGuard(func(context.Context, string, string) error { return nil }),
	)
	requirePerm := interceptor.RequirePermissionUnary("orders:write")

	called := false
	_, err = authB(mtlsCtx, nil, &grpc.UnaryServerInfo{FullMethod: "/svc.B/Write"},
		func(ctx context.Context, req any) (any, error) {
			return requirePerm(ctx, req, &grpc.UnaryServerInfo{FullMethod: "/svc.B/Write"}, func(ctx context.Context, _ any) (any, error) {
				called = true
				assert.True(t, interceptor.IsTrustedS2S(ctx))
				assert.Equal(t, subject, interceptor.UserID(ctx))
				assert.Equal(t, []string{"orders:write"}, interceptor.UserPermissions(ctx))
				assert.Equal(t, "api:write", interceptor.UserScopes(ctx))
				return "ok", nil
			})
		},
	)
	require.NoError(t, err)
	assert.True(t, called)
}

// TestCrossHop_RequirePermission_DeniesWithoutPropagatedEntitlements is the
// permission-laundering regression on the gRPC path.
func TestCrossHop_RequirePermission_DeniesWithoutPropagatedEntitlements(t *testing.T) {
	provider, _ := testKeyAndProvider(t)
	cert := &x509.Certificate{
		Subject:     pkix.Name{CommonName: "svc-a"},
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	md := metadata.Pairs("x-user-id", "11111111-1111-1111-1111-111111111111")
	mtlsCtx := metadata.NewIncomingContext(context.Background(), md)
	state := tls.ConnectionState{
		PeerCertificates: []*x509.Certificate{cert},
		VerifiedChains:   [][]*x509.Certificate{{cert}},
	}
	mtlsCtx = peer.NewContext(mtlsCtx, &peer.Peer{AuthInfo: credentials.TLSInfo{State: state}})

	authB := interceptor.MTLSAuthUnary(provider,
		interceptor.WithAllowedCNs("svc-a"),
		interceptor.WithS2SImpersonationGuard(func(context.Context, string, string) error { return nil }),
	)
	requirePerm := interceptor.RequirePermissionUnary("orders:write")

	called := false
	_, err := authB(mtlsCtx, nil, &grpc.UnaryServerInfo{FullMethod: "/svc.B/Write"},
		func(ctx context.Context, req any) (any, error) {
			return requirePerm(ctx, req, &grpc.UnaryServerInfo{FullMethod: "/svc.B/Write"}, func(context.Context, any) (any, error) {
				called = true
				return "ok", nil
			})
		},
	)
	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.PermissionDenied, st.Code())
	assert.False(t, called)
}

// TestCrossHop_RequireScope_UsesPropagatedScopes mirrors the permission hop
// test for scopes.
func TestCrossHop_RequireScope_UsesPropagatedScopes(t *testing.T) {
	subject := "11111111-1111-1111-1111-111111111111"
	provider, privKey := testKeyAndProvider(t)
	token := signTestTokenWithClaims(t, privKey, map[string]any{
		"sub":    subject,
		"scopes": "api:write billing:read",
	})
	unary := interceptor.AuthUnary(provider)
	var hop1 context.Context
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer "+token))
	_, err := unary(ctx, nil, &grpc.UnaryServerInfo{FullMethod: "/svc.A/Handle"}, func(c context.Context, _ any) (any, error) {
		hop1 = c
		return nil, nil
	})
	require.NoError(t, err)

	outMD, _ := metadata.FromOutgoingContext(interceptor.AppendOutgoingIdentity(hop1))
	cert := &x509.Certificate{
		Subject:     pkix.Name{CommonName: "svc-a"},
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	mtlsCtx := metadata.NewIncomingContext(context.Background(), outMD.Copy())
	state := tls.ConnectionState{
		PeerCertificates: []*x509.Certificate{cert},
		VerifiedChains:   [][]*x509.Certificate{{cert}},
	}
	mtlsCtx = peer.NewContext(mtlsCtx, &peer.Peer{AuthInfo: credentials.TLSInfo{State: state}})

	authB := interceptor.MTLSAuthUnary(provider,
		interceptor.WithAllowedCNs("svc-a"),
		interceptor.WithS2SImpersonationGuard(func(context.Context, string, string) error { return nil }),
	)
	requireScope := interceptor.RequireScopeUnary("api:write")

	called := false
	_, err = authB(mtlsCtx, nil, &grpc.UnaryServerInfo{FullMethod: "/svc.B/Write"},
		func(ctx context.Context, req any) (any, error) {
			return requireScope(ctx, req, &grpc.UnaryServerInfo{FullMethod: "/svc.B/Write"}, func(context.Context, any) (any, error) {
				called = true
				return "ok", nil
			})
		},
	)
	require.NoError(t, err)
	assert.True(t, called)
}
