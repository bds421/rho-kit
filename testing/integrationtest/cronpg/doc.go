// Package cronpg contains PostgreSQL-backed integration tests for the
// data/cron/pgstore package — schedule persistence and CRUD behaviour
// over Add/Get/List/Upsert/Enable/Remove, including duplicate-Add
// failures, idempotent Remove, and ErrScheduleNotFound on unknown names.
//
// All tests run only under `//go:build integration` so the default
// `go test ./...` stays Docker-free.
package cronpg
