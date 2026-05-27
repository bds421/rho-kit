// Package cronpg contains PostgreSQL-backed integration tests for the
// data/cron/pgstore package — schedule persistence, optimistic-ish
// idempotency on Add/Upsert/Remove/Enable, ApplyTo round-trip to a
// runtime/cron.Scheduler with a synthetic jobs map.
//
// All tests run only under `//go:build integration` so the default
// `go test ./...` stays Docker-free.
package cronpg
