// Package localbackend provides a local filesystem implementation of [storage.Storage].
//
// It is intended for development and testing only; production multi-instance
// deployments should use S3 or another shared backend. Keys map to relative
// file paths within a root directory, and directory components are created
// automatically on Put.
//
// # Object metadata is not persisted
//
// This backend stores only the object bytes on disk. It does NOT persist
// [storage.ObjectMeta] fields such as ContentType or Custom: Put validates the
// metadata but discards it, Get returns only Size (derived from the file), and
// List never populates ContentType. This diverges from in-memory and S3
// backends, which round-trip ContentType and Custom. Code that relies on
// metadata round-tripping (for example serving the stored Content-Type) must
// not depend on localbackend for that behavior.
package localbackend
