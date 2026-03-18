// Package sftpbackend provides an SFTP implementation of [storage.Storage].
//
// It manages an SSH connection with automatic reconnection on failure,
// mirroring the pattern used by the Redis connection in the kit. All
// operations are instrumented with Prometheus metrics and OpenTelemetry traces.
package sftpbackend
