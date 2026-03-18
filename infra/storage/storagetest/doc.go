// Package storagetest provides test helpers and a shared compliance suite
// for [storage.Storage] implementations.
//
// NewLocalBackend creates a temporary filesystem-backed backend suitable for
// unit tests. BackendSuite runs a standard set of tests against any backend
// to verify correct behavior across implementations.
//
// Integration test helpers (StartS3, StartSFTP) are guarded by the
// "integration" build tag and require Docker.
package storagetest
