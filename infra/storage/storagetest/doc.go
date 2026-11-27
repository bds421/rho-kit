// Package storagetest provides test helpers and a shared compliance suite
// for [storage.Storage] implementations.
//
// NewLocalBackend creates a temporary filesystem-backed backend suitable for
// unit tests. BackendSuite and ListerSuite run a standard set of tests
// against any backend and delete every key they wrote when the test ends.
// NewS3Bucket creates a bucket of the test's own on a given S3 endpoint and
// removes it, objects first, when the test ends.
//
// Integration test helpers (StartS3, StartSFTP) are guarded by the
// "integration" build tag and require Docker.
package storagetest
