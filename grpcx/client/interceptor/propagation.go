package interceptor

import (
	"context"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	"github.com/bds421/rho-kit/core/v2/contextutil"
	srvinterceptor "github.com/bds421/rho-kit/grpcx/v2/interceptor"
)

// Metadata key names mirror the server-side interceptor so
// correlation/request IDs propagate end-to-end across hops.
const (
	correlationIDKey = "x-correlation-id"
	requestIDKey     = "x-request-id"
)

// PropagationOption configures identity/correlation injection.
type PropagationOption func(*propagationConfig)

type propagationConfig struct {
	disableIdentity bool
}

// WithoutIdentity disables stamping authenticated subject/actor identity
// onto outgoing metadata. Correlation and request IDs still propagate.
func WithoutIdentity() PropagationOption {
	return func(c *propagationConfig) { c.disableIdentity = true }
}

// PropagationUnaryClientInterceptor returns a unary client interceptor
// that copies the kit correlation_id + request_id from the caller's ctx
// into the outgoing gRPC metadata so the server-side interceptors see
// them. This runs unconditionally in [client.NewClient] — independent of
// logging — so disabling logging never severs end-to-end trace joins.
//
// By default verified identity (x-user-id / x-subject-id / x-actor-*) is
// also forwarded. Pass [WithoutIdentity] (or client.WithoutIdentityPropagation)
// when dialing untrusted targets.
//
// If an ID is not present on ctx, nothing is added: the server's
// adoptOrGenerate allocates one. Existing metadata values are preserved
// and never overwritten.
func PropagationUnaryClientInterceptor(opts ...PropagationOption) grpc.UnaryClientInterceptor {
	cfg := propagationConfig{}
	for _, o := range opts {
		if o == nil {
			panic("client/interceptor: PropagationUnary option must not be nil")
		}
		o(&cfg)
	}
	return func(
		ctx context.Context,
		method string,
		req, reply any,
		cc *grpc.ClientConn,
		invoker grpc.UnaryInvoker,
		callOpts ...grpc.CallOption,
	) error {
		ctx = injectPropagation(ctx, cfg.disableIdentity)
		return invoker(ctx, method, req, reply, cc, callOpts...)
	}
}

// PropagationStreamClientInterceptor mirrors
// [PropagationUnaryClientInterceptor] for streaming RPCs.
func PropagationStreamClientInterceptor(opts ...PropagationOption) grpc.StreamClientInterceptor {
	cfg := propagationConfig{}
	for _, o := range opts {
		if o == nil {
			panic("client/interceptor: PropagationStream option must not be nil")
		}
		o(&cfg)
	}
	return func(
		ctx context.Context,
		desc *grpc.StreamDesc,
		cc *grpc.ClientConn,
		method string,
		streamer grpc.Streamer,
		callOpts ...grpc.CallOption,
	) (grpc.ClientStream, error) {
		ctx = injectPropagation(ctx, cfg.disableIdentity)
		return streamer(ctx, desc, cc, method, callOpts...)
	}
}

// injectPropagation copies kit correlation/request IDs and optionally
// verified identity from ctx into outgoing metadata. Existing values are
// left untouched.
func injectPropagation(ctx context.Context, disableIdentity bool) context.Context {
	md, _ := metadata.FromOutgoingContext(ctx)
	if md == nil {
		md = metadata.MD{}
	} else {
		md = md.Copy()
	}
	if cid := contextutil.CorrelationID(ctx); cid != "" && len(md.Get(correlationIDKey)) == 0 {
		md.Set(correlationIDKey, cid)
	}
	if rid := contextutil.RequestID(ctx); rid != "" && len(md.Get(requestIDKey)) == 0 {
		md.Set(requestIDKey, rid)
	}
	ctx = metadata.NewOutgoingContext(ctx, md)
	if disableIdentity {
		return ctx
	}
	return srvinterceptor.AppendOutgoingIdentity(ctx)
}
