// Package interceptor holds the client-side gRPC interceptors used by
// [github.com/bds421/rho-kit/grpcx/v2/client]. Each exposes a unary and
// stream variant where applicable.
//
// Chained automatically by [client.NewClient] (outermost first):
//
//	recovery -> logging -> metrics -> retry -> deadline -> caller -> RPC
//
// Each interceptor is independently usable for callers building a
// custom chain via [client.WithUnaryInterceptors] / [client.WithStreamInterceptors].
package interceptor
