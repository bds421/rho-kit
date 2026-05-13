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
//
// # Backend-Prefixed Type Names
//
// The backend-specific implementations (`s3backend.S3Backend`,
// `azurebackend.AzureBackend`, `gcsbackend.GCSBackend`,
// `sftpbackend.SFTPBackend`) deliberately keep the cloud/protocol prefix in
// their exported type names. Go's package-name lint will flag this as
// stutter and tempt future contributors to rename them to plain `Backend`.
//
// Do NOT do that. Wiring code routinely dot-imports two or more backend
// packages in the same file (e.g. a manager that needs both S3 and SFTP).
// With plain `Backend`, the imports collide and the call site has to alias
// every package — a worse readability outcome than the prefix stutter on a
// fully-qualified name. The prefix is a feature: it stays embedded so the
// type name is self-describing wherever it appears (`s3backend.S3Backend`
// in code, "S3Backend" in error messages and stack traces).
package storage
