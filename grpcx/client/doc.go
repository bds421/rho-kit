// Package client provides the kit's gRPC client construction surface.
// It is the symmetric counterpart of [github.com/bds421/rho-kit/grpcx/v2]
// (server) — same opinionated defaults, mirrored option vocabulary,
// shared dependency closure.
//
// # Use this package when
//
//   - You need to dial a gRPC server from kit-managed code.
//   - You want the kit's mTLS chain (loopback-only insecure opt-out
//     panics on non-loopback addresses), default per-RPC deadline,
//     keepalive, chained recovery/retry/logging/metrics interceptors,
//     and OTel propagation without writing the boilerplate per service.
//
// # Do NOT use this package for
//
//   - Constructing a *grpc.Server. Use [grpcx.NewServer] instead.
//   - Streaming retries. [WithRetry] applies to unary RPCs only;
//     gRPC retry semantics for streams are fundamentally different
//     (must restart before any message has been received) and the
//     stream-retry path is intentionally opt-in via a custom
//     [WithStreamInterceptors] chain.
//
// # Sibling packages
//
//   - [github.com/bds421/rho-kit/grpcx/v2]               — gRPC server.
//   - [github.com/bds421/rho-kit/grpcx/v2/interceptor]   — server-side
//     interceptors (chained automatically by NewServer).
//   - [github.com/bds421/rho-kit/grpcx/v2/client/interceptor] — THIS
//     package's client-side interceptors (recovery / retry / logging /
//     metrics / deadline). Exposed for callers building a custom chain.
//
// # Quick start
//
//	conn, err := client.NewClient("api.internal:443",
//	    client.WithTLSConfig(infraTLS),       // kit-resolved mTLS
//	    client.WithDefaultTimeout(5*time.Second),
//	)
//	if err != nil {
//	    return err
//	}
//	defer conn.Close()
//
//	c := pb.NewMyServiceClient(conn)
//	resp, err := c.GetThing(ctx, &pb.GetThingRequest{ID: "abc"})
package client
