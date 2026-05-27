// Package sagapg contains PostgreSQL-backed integration tests for the
// data/saga/pgstore package — the strict-concurrency Put split
// (INSERT…ON CONFLICT DO NOTHING for first-write, UPDATE…WHERE
// updated_at=$old for state-advance, no IS NULL escape) + Get / List /
// ListResumable / Delete round-trips.
//
// All tests run only under `//go:build integration` so the default
// `go test ./...` stays Docker-free.
package sagapg
