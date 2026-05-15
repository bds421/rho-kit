package grpcx_test

import (
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/keepalive"
	reflectionpb "google.golang.org/grpc/reflection/grpc_reflection_v1"
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

// TestNewServer_RawOptionsDoNotOverrideHardenedDefaults guards L083:
// a caller passing raw grpc.ServerOption values via WithGRPCServerOptions
// must NOT be able to override kit-hardened defaults like
// MaxRecvMsgSize. The kit re-appends its hardened set AFTER cfg.grpcOpts
// so grpc.NewServer's slice-order semantics put the kit's values last.
//
// This is verified by sending a payload that exceeds the kit's
// default 4 MiB MaxRecvMsgSize but is well within the raw override's
// 64 MiB value. The server must reject with ResourceExhausted
// (proving the kit default won), not accept it.
func TestNewServer_RawOptionsDoNotOverrideHardenedDefaults(t *testing.T) {
	const bufSize = 8 * 1024 * 1024
	lis := bufconn.Listen(bufSize)

	// Caller attempts to widen MaxRecvMsgSize to 64 MiB via raw
	// option. The kit's hardened default (4 MiB) must still apply.
	srv := grpcx.NewServer(
		grpcx.WithGRPCServerOptions(grpc.MaxRecvMsgSize(64 * 1024 * 1024)),
	)
	healthpb.RegisterHealthServer(srv, &okHealth{})

	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.GracefulStop)

	conn, err := grpc.NewClient("passthrough:///bufconn",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		// Client side must also be permissive — otherwise the
		// client transport rejects the message before it leaves.
		grpc.WithDefaultCallOptions(grpc.MaxCallSendMsgSize(64*1024*1024)),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	client := healthpb.NewHealthClient(conn)

	// 5 MiB payload — exceeds the kit's hardened 4 MiB default but
	// inside the raw 64 MiB override.
	big := strings.Repeat("x", 5*1024*1024)
	_, err = client.Check(context.Background(), &healthpb.HealthCheckRequest{Service: big})
	require.Error(t, err, "request larger than kit default must be rejected even when raw option attempts to widen")
	require.Equal(t, codes.ResourceExhausted, status.Code(err),
		"hardened default must produce ResourceExhausted; raw override should NOT win")
}

// TestNewServer_ReflectionDisabledByDefault locks the security default:
// kit-built servers must NOT expose the reflection service unless the
// caller opts in via WithReflection. Reflection leaks every registered
// proto descriptor, so the default-off posture protects services that
// forget to think about it.
func TestNewServer_ReflectionDisabledByDefault(t *testing.T) {
	lis := bufconn.Listen(1 << 20)
	srv := grpcx.NewServer()
	healthpb.RegisterHealthServer(srv, &okHealth{})

	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.GracefulStop)

	conn, err := grpc.NewClient("passthrough:///bufconn",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	client := reflectionpb.NewServerReflectionClient(conn)
	stream, err := client.ServerReflectionInfo(context.Background())
	require.NoError(t, err)
	// Send may itself return io.EOF when the server has already
	// rejected the stream — in which case the status comes from Recv
	// or from CloseSend. Either way, the test must see Unimplemented,
	// never a successful descriptor response.
	if sendErr := stream.Send(&reflectionpb.ServerReflectionRequest{
		MessageRequest: &reflectionpb.ServerReflectionRequest_ListServices{ListServices: ""},
	}); sendErr != nil && !errors.Is(sendErr, io.EOF) {
		require.Equal(t, codes.Unimplemented, status.Code(sendErr),
			"unregistered reflection service must surface Unimplemented; got %v", sendErr)
		return
	}
	resp, err := stream.Recv()
	require.Nil(t, resp, "reflection must NOT return a descriptor response when WithReflection is unset")
	require.Error(t, err, "reflection must NOT respond when WithReflection is not set")
	if errors.Is(err, io.EOF) {
		// Stream closed without a server message — exercise CloseSend
		// to surface the final status from the trailers.
		closeErr := stream.CloseSend()
		if closeErr != nil && !errors.Is(closeErr, io.EOF) {
			require.Equal(t, codes.Unimplemented, status.Code(closeErr),
				"closing the unregistered reflection stream must surface Unimplemented; got %v", closeErr)
			return
		}
		// EOF with no recoverable status means the server treated the
		// stream as terminated without dispatching to a reflection
		// handler — which is the security property we wanted.
		return
	}
	require.Equal(t, codes.Unimplemented, status.Code(err),
		"unregistered reflection service must surface Unimplemented; got %v", err)
}

// TestNewServer_WithReflection verifies opt-in registration: when the
// caller passes WithReflection, the reflection service answers and
// includes the caller-registered services in its ListServices response.
func TestNewServer_WithReflection(t *testing.T) {
	lis := bufconn.Listen(1 << 20)
	srv := grpcx.NewServer(grpcx.WithReflection())
	healthpb.RegisterHealthServer(srv, &okHealth{})

	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.GracefulStop)

	conn, err := grpc.NewClient("passthrough:///bufconn",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	client := reflectionpb.NewServerReflectionClient(conn)
	stream, err := client.ServerReflectionInfo(context.Background())
	require.NoError(t, err)
	require.NoError(t, stream.Send(&reflectionpb.ServerReflectionRequest{
		MessageRequest: &reflectionpb.ServerReflectionRequest_ListServices{ListServices: ""},
	}))
	resp, err := stream.Recv()
	require.NoError(t, err, "reflection must respond when WithReflection is set")

	list := resp.GetListServicesResponse()
	require.NotNil(t, list, "expected ListServicesResponse, got %T", resp.GetMessageResponse())

	var saw bool
	for _, svc := range list.GetService() {
		if svc.GetName() == "grpc.health.v1.Health" {
			saw = true
			break
		}
	}
	assert.True(t, saw, "reflection must enumerate caller-registered services; got %v", list.GetService())
}

type okHealth struct {
	healthpb.UnimplementedHealthServer
}

func (okHealth) Check(context.Context, *healthpb.HealthCheckRequest) (*healthpb.HealthCheckResponse, error) {
	return &healthpb.HealthCheckResponse{Status: healthpb.HealthCheckResponse_SERVING}, nil
}
