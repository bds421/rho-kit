// Package integrationtest is the umbrella module for all kit
// integration tests that require Docker-backed dependencies
// (Postgres, Redis, AMQP, S3, etc.). Each subpackage holds the
// integration suite for one kit module — e.g. ./redisqueue for
// data/queue/redisqueue, ./pgadvisory for infra/leaderelection/pgadvisory.
//
// Wave 138 (kittest) merged the testing-helper modules. Wave 154
// (this module) merges the integration-test modules so a single
// `go test -tags integration ./testing/integrationtest/...`
// exercises every kit primitive end-to-end against real backends.
// Prior to wave 154 each integrationtest lived in its own go.mod
// submodule, inflating the kit's module count by ~25.
//
// All tests in this module are guarded by the `integration` build
// tag and skipped from the default `go test ./...` run.
package integrationtest
