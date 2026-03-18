// Package localbackend provides a local filesystem implementation of [storage.Storage].
//
// It is intended for development and testing only; production multi-instance
// deployments should use S3 or another shared backend. Keys map to relative
// file paths within a root directory, and directory components are created
// automatically on Put.
package localbackend
