// Package grpc wires a public gRPC server into the Builder via the v2.0.0
// lazy-adapter Module API. Importing this package is the explicit
// declaration that the service exposes gRPC; HTTP-only services can omit it
// and avoid pulling google.golang.org/grpc plus otelgrpc into their binary.
//
// Usage:
//
//	import (
//	    "github.com/bds421/rho-kit/app/v2"
//	    "github.com/bds421/rho-kit/app/grpc/v2"
//	)
//
//	app.New("svc", "v1", base).
//	    With(grpc.Module(registerSvcs, ":50051")).
//	    Router(...).
//	    Run()
//
// The Module performs three integrations beyond running the listener:
//
//  1. TLS auto-wire: when the Builder resolves a kit-level serverTLS, the
//     same credentials are applied to the gRPC listener via grpc.Creds so
//     services that set TLS_CERT/TLS_KEY don't silently run plaintext gRPC.
//
//  2. Internal gRPC health: when this module is registered, the internal
//     ops port additionally serves the gRPC Health Checking Protocol over
//     h2c on the same address as HTTP /ready. Internal callers can probe
//     either protocol. Public gRPC health is off by default and requires
//     [WithPublicHealth].
//
//  3. Lifecycle attachment: the gRPC server is registered with the
//     lifecycle Runner so it is stopped AFTER the public HTTP server
//     (reverse registration order) during graceful shutdown.
package grpc
