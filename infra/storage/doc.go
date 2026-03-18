// Package storage defines a backend-agnostic object storage interface
// and composable upload validators.
//
// The Storage interface follows the same pattern as [cache.Cache]: a narrow,
// four-method contract (Put, Get, Delete, Exists) with sentinel errors for
// common conditions. Backends like S3, local filesystem, and SFTP implement
// this interface and are swappable without changing application code.
//
// Upload validation is handled by composable [Validator] functions that
// inspect the byte stream before it reaches the backend. Validators never
// buffer the entire file — they wrap the reader (e.g. with size limits or
// MIME-type sniffing) so bytes flow directly from the source to the backend.
//
// Optional capabilities like pre-signed URLs are exposed through separate
// interfaces (e.g. [PresignedStore]) and checked at the call site via type
// assertion.
package storage
