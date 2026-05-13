// Package postgres wires a pgx-native Postgres pool into the Builder via the
// v2.0.0 lazy-adapter Module API. Importing this package is the explicit
// declaration that the service needs Postgres; services that only speak HTTP
// can omit it and avoid pulling pgx into their binary.
//
// Usage:
//
//	import (
//	    "github.com/bds421/rho-kit/app/v2"
//	    "github.com/bds421/rho-kit/app/postgres/v2"
//	    pgxbackend "github.com/bds421/rho-kit/infra/sqldb/pgx/v2"
//	)
//
//	app.New("svc", "v1", base).
//	    With(postgres.Module(pgxbackend.Config{DSN: cfg.DSN})).
//	    Router(func(infra app.Infrastructure) http.Handler {
//	        pool := postgres.Pool(infra)
//	        // …query the pool…
//	    }).
//	    Run()
//
// Migrations are configured via [WithMigrations]; they run inside the module's
// Init using goose so the schema is up to date before the service serves
// traffic. Failure to apply migrations aborts startup.
//
// Failure semantics: Module panics if cfg.DSN is empty; the pgx package's
// Connect rejects non-loopback DSNs that disable TLS so misconfigured
// sslmode values fail closed.
package postgres
