// Package gcsbackend provides a Google Cloud Storage implementation of
// [storage.Storage].
//
// It uses the official cloud.google.com/go/storage SDK. Authentication is
// handled via Application Default Credentials (ADC) or a service account
// JSON key file.
//
// All operations are instrumented with Prometheus metrics and OpenTelemetry
// traces.
//
// Listing is intentionally unsupported: this package does not implement
// [storage.Lister]. Type-asserting Lister against a gcsbackend.Backend
// returns false. Use s3backend, sftpbackend, localbackend, or membackend
// when object listing is required (see docs/ai/storage.md capability matrix).
package gcsbackend
