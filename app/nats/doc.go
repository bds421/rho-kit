// Package nats wires a NATS JetStream connection into the Builder via the
// v2.0.0 lazy-adapter Module API. Importing this package is the explicit
// declaration that the service needs NATS; HTTP-only services can omit it
// and avoid pulling nats.go into their binary.
//
// Usage:
//
//	import (
//	    "github.com/bds421/rho-kit/app/v2"
//	    "github.com/bds421/rho-kit/app/nats/v2"
//	    natsbackend "github.com/bds421/rho-kit/infra/messaging/natsbackend/v2"
//	)
//
//	app.New("svc", "v1", base).
//	    With(nats.Module(natsbackend.Config{URL: cfg.NATSURL})).
//	    Router(func(infra app.Infrastructure) http.Handler {
//	        pub := nats.Publisher(infra)
//	        // …publish messages…
//	    }).
//	    Run()
//
// JetStream stream/consumer declarations remain caller-driven (via
// natsbackend.Connection.EnsureStream inside the router) so the Module does
// not impose a stream topology.
package nats
