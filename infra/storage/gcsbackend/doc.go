// Package gcsbackend provides a Google Cloud Storage implementation of
// [storage.Storage].
//
// It uses the official cloud.google.com/go/storage SDK. Authentication is
// handled via Application Default Credentials (ADC) or a service account
// JSON key file.
//
// All operations are instrumented with OpenTelemetry traces.
package gcsbackend
