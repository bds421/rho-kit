// Package amqp wires a RabbitMQ connection (publisher + consumer) into the
// Builder via the v2.0.0 lazy-adapter Module API. Importing this package is
// the explicit declaration that the service needs RabbitMQ; HTTP-only
// services can omit it and avoid pulling amqp091-go into their binary.
//
// Usage:
//
//	import (
//	    "github.com/bds421/rho-kit/app/v2"
//	    "github.com/bds421/rho-kit/app/amqp/v2"
//	)
//
//	app.New("svc", "v1", base).
//	    With(amqp.Module(cfg.RabbitMQURL)).
//	    Router(func(infra app.Infrastructure) http.Handler {
//	        pub := amqp.Publisher(infra)
//	        // …publish messages…
//	    }).
//	    Run()
//
// Transport safety: the Module rejects non-loopback `amqp://` URLs (plaintext)
// at construction time unless [WithoutTLS] is passed. Local-dev fixtures (the
// URL host resolves to loopback) bypass the check. See [Module] for the
// full safety contract.
package amqp
