// Package kittest is a re-export umbrella for the kit's testing-helper
// packages. Consumer services can depend on one umbrella module instead of
// importing each underlying testing module individually.
//
// Subpackages:
//
//   - testing/kittest/db    — re-exports infra/sqldb/dbtest    (Postgres testcontainer)
//   - testing/kittest/redis — re-exports infra/redis/redistest (Redis testcontainer)
//   - testing/kittest/storage — re-exports infra/storage/storagetest (storage backend
//     compliance suites, local helper, optional S3/SFTP testcontainers)
//   - testing/kittest/amqp  — re-exports integrated rabbitmqtest fixture
//     (RabbitMQ testcontainer)
//
// All re-exports are zero-cost: type aliases preserve identity and method
// sets; var aliases reuse the original function values. Direct imports of the
// underlying modules still work — the umbrella is additive.
//
// The integration-tagged helpers (StartPostgres, redistest.Start, FlushDB,
// StartS3, StartSFTP, rabbitmqtest.Start) live under the `integration` build
// tag in their source modules, so the umbrella re-exports them under the same
// tag. Tests that use these helpers must compile under `-tags integration`.
package kittest
