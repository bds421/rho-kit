// Package postgres is the pgx-backed [apikey.Repository].
//
// It looks keys up by their public id (the primary key), which is the id
// embedded in the token — a single indexed read followed by one
// constant-time hash compare in [apikey.Key.Verify]. The secret is never
// stored; only its SHA-256 hash lives in the hash column.
//
// Schema lives in [Migrations]; embed it into the service's migration set
// or apply it with the kit migrate tool.
package postgres
