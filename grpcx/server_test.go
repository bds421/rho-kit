package grpcx_test

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"github.com/bds421/rho-kit/grpcx/v2"
)

func TestNewServer_DefaultsDoNotPanic(t *testing.T) {
	srv := grpcx.NewServer()
	require.NotNil(t, srv)
	srv.Stop()
}

func TestNewServer_WithOptions(t *testing.T) {
	srv := grpcx.NewServer(
		grpcx.WithMaxRecvMsgSize(8<<20),
		grpcx.WithMaxSendMsgSize(8<<20),
		grpcx.WithKeepaliveParams(keepalive.ServerParameters{
			MaxConnectionIdle: 10 * time.Minute,
		}),
		grpcx.WithKeepalivePolicy(keepalive.EnforcementPolicy{
			MinTime: 1 * time.Minute,
		}),
	)
	require.NotNil(t, srv)
	srv.Stop()
}

func TestNewServer_WithInterceptors(t *testing.T) {
	srv := grpcx.NewServer(
		grpcx.WithUnaryInterceptors(func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
			return handler(ctx, req)
		}),
		grpcx.WithStreamInterceptors(func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
			return handler(srv, ss)
		}),
		grpcx.WithGRPCServerOptions(),
	)
	require.NotNil(t, srv)
	srv.Stop()
}

func TestWithMaxRecvMsgSize_PanicsOnZero(t *testing.T) {
	assert.Panics(t, func() {
		grpcx.WithMaxRecvMsgSize(0)
	})
}

func TestWithMaxConcurrentStreams_PanicsOnZero(t *testing.T) {
	// 0 means "unlimited" in the gRPC framework — exactly the GAP-03
	// regression the option exists to prevent. Reject loudly at the call
	// site rather than silently turning hardening off.
	assert.Panics(t, func() {
		grpcx.WithMaxConcurrentStreams(0)
	})
}

func TestWithMaxRecvMsgSize_PanicsOnNegative(t *testing.T) {
	assert.Panics(t, func() {
		grpcx.WithMaxRecvMsgSize(-1)
	})
}

func TestWithMaxSendMsgSize_PanicsOnZero(t *testing.T) {
	assert.Panics(t, func() {
		grpcx.WithMaxSendMsgSize(0)
	})
}

func TestNewServer_PanicsOnNilOption(t *testing.T) {
	assert.Panics(t, func() {
		grpcx.NewServer(nil)
	})
}

func TestWithUnaryInterceptors_PanicsOnNil(t *testing.T) {
	assert.Panics(t, func() {
		grpcx.WithUnaryInterceptors(nil)
	})
}

func TestWithStreamInterceptors_PanicsOnNil(t *testing.T) {
	assert.Panics(t, func() {
		grpcx.WithStreamInterceptors(nil)
	})
}

func TestWithGRPCServerOptions_PanicsOnNil(t *testing.T) {
	assert.Panics(t, func() {
		grpcx.WithGRPCServerOptions(nil)
	})
}

// panickingHealth panics on Check — used to verify NewServer's default
// recovery interceptor catches panics without an explicit Recovery option.
type panickingHealth struct {
	healthpb.UnimplementedHealthServer
}

func (panickingHealth) Check(context.Context, *healthpb.HealthCheckRequest) (*healthpb.HealthCheckResponse, error) {
	panic("kit-default recovery should catch this")
}

func TestNewServer_RecoveryEnabledByDefault(t *testing.T) {
	const bufSize = 1024 * 1024
	lis := bufconn.Listen(bufSize)

	srv := grpcx.NewServer()
	healthpb.RegisterHealthServer(srv, panickingHealth{})

	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.GracefulStop)

	conn, err := grpc.NewClient("passthrough:///bufconn",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	_, err = healthpb.NewHealthClient(conn).Check(context.Background(), &healthpb.HealthCheckRequest{})
	require.Error(t, err)

	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.Internal, st.Code(), "kit-default recovery should convert panic to codes.Internal")
}

func TestNewServer_WithoutRecovery_NonPanickingRPCStillWorks(t *testing.T) {
	// We can't safely run a panicking handler with recovery off — the panic
	// crashes the test process before the assertion runs. The contract for
	// WithoutRecovery is "panics propagate", which we verify indirectly: the
	// server still starts and serves a non-panicking RPC, so the option
	// merely toggles the interceptor (rather than misconfiguring the chain).
	const bufSize = 1024 * 1024
	lis := bufconn.Listen(bufSize)

	srv := grpcx.NewServer(grpcx.WithoutRecovery())
	healthpb.RegisterHealthServer(srv, &healthpb.UnimplementedHealthServer{})

	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.GracefulStop)

	conn, err := grpc.NewClient("passthrough:///bufconn",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	_, err = healthpb.NewHealthClient(conn).Check(context.Background(), &healthpb.HealthCheckRequest{})
	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.Unimplemented, st.Code(),
		"WithoutRecovery removes the recovery interceptor only — the rest of the chain stays functional")
}

// blockingWatchHealth keeps each Watch RPC parked on the server until the
// client cancels its context, which lets the stream-cap test hold N
// concurrent streams open against a single connection.
type blockingWatchHealth struct {
	healthpb.UnimplementedHealthServer
	started chan struct{}
}

func (b *blockingWatchHealth) Watch(_ *healthpb.HealthCheckRequest, srv healthpb.Health_WatchServer) error {
	select {
	case b.started <- struct{}{}:
	default:
	}
	<-srv.Context().Done()
	return status.Error(codes.Canceled, "client cancelled")
}

func TestNewServer_MaxConcurrentStreamsCapsInflightStreams(t *testing.T) {
	// Build a server capped at 2 concurrent streams per connection, hold
	// 2 streams open, then start a 3rd Watch — it must block in transport
	// pending a slot. Releasing one open stream lets the 3rd proceed,
	// proving the cap (and not some unrelated client serialisation)
	// gates new streams.
	const bufSize = 1024 * 1024
	const streamCap = 2

	lis := bufconn.Listen(bufSize)
	health := &blockingWatchHealth{started: make(chan struct{}, streamCap+1)}

	srv := grpcx.NewServer(
		grpcx.WithMaxConcurrentStreams(streamCap),
		// The 30s default-deadline interceptor would race the test if
		// the test machine is heavily loaded; disabling it keeps the
		// only timeout the test's own client-side contexts.
		grpcx.WithoutDefaultDeadline(),
	)
	healthpb.RegisterHealthServer(srv, health)

	serveDone := make(chan struct{})
	go func() {
		defer close(serveDone)
		_ = srv.Serve(lis)
	}()

	conn, err := grpc.NewClient("passthrough:///bufconn",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err)

	// Track every client-side cancel so the test always returns the
	// server to a quiescent state — GracefulStop below blocks on any
	// stream that is still parked in the handler.
	var allCancels []context.CancelFunc
	t.Cleanup(func() {
		for _, c := range allCancels {
			c()
		}
		_ = conn.Close()
		// Force a hard Stop on shutdown — GracefulStop would deadlock
		// if a handler cancel race left a stream parked. Stop is the
		// correct shutdown semantic for a test that intentionally
		// holds streams open.
		srv.Stop()
		<-serveDone
	})

	client := healthpb.NewHealthClient(conn)

	// Open `streamCap` long-running Watch streams and confirm each
	// reaches its handler.
	for i := 0; i < streamCap; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		_, err := client.Watch(ctx, &healthpb.HealthCheckRequest{})
		require.NoError(t, err)
		allCancels = append(allCancels, cancel)
		select {
		case <-health.started:
		case <-time.After(2 * time.Second):
			t.Fatalf("Watch #%d did not start on the server", i+1)
		}
	}

	// The (streamCap+1)th stream must block in the client transport
	// (`http2Client.NewStream` waits for a slot when the server's
	// SETTINGS_MAX_CONCURRENT_STREAMS budget is saturated) and must NOT
	// reach the server handler.
	thirdCtx, thirdCancel := context.WithCancel(context.Background())
	allCancels = append(allCancels, thirdCancel)
	thirdOpened := make(chan struct{})
	go func() {
		defer close(thirdOpened)
		_, _ = client.Watch(thirdCtx, &healthpb.HealthCheckRequest{})
	}()

	select {
	case <-health.started:
		t.Fatal("MaxConcurrentStreams=2 did not gate the 3rd stream — handler started while 2 streams were already in flight")
	case <-thirdOpened:
		t.Fatal("MaxConcurrentStreams=2 did not gate the 3rd stream — client.Watch returned while 2 streams were already in flight")
	case <-time.After(200 * time.Millisecond):
		// Expected: the 3rd stream is queued at the transport layer.
	}

	// Release one stream; the 3rd should now make it past the
	// transport and reach the handler.
	allCancels[0]()
	select {
	case <-health.started:
		// Expected.
	case <-time.After(2 * time.Second):
		t.Fatal("3rd Watch never started after freeing a slot — cap may be too tight or release did not propagate")
	}
}
