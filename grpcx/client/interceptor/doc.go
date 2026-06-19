// Package interceptor holds the client-side gRPC interceptors used by
// [github.com/bds421/rho-kit/grpcx/v2/client]. Each exposes a unary and
// stream variant where applicable.
//
// Chained automatically by [client.NewClient] (outermost first):
//
//	recovery -> propagation -> logging -> metrics -> retry -> deadline -> caller -> RPC
//
// The propagation pair ([PropagationUnaryClientInterceptor] /
// [PropagationStreamClientInterceptor]) is always on, so correlation and
// request IDs reach the server even when logging is disabled.
//
// Each interceptor is independently usable for callers building a
// custom chain via [client.WithUnaryInterceptors] / [client.WithStreamInterceptors].
package interceptor
