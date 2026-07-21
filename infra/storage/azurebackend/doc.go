// Package azurebackend provides an Azure Blob Storage implementation of
// [storage.Storage].
//
// It uses the official Azure SDK for Go (azblob) and supports Azure
// Storage accounts, Azure Government, and Azurite for local development.
//
// All operations are instrumented with Prometheus metrics and OpenTelemetry
// traces.
//
// Listing is intentionally unsupported: this package does not implement
// [storage.Lister]. Type-asserting Lister against an azurebackend.Backend
// returns false. Use s3backend, sftpbackend, localbackend, or membackend
// when object listing is required (see docs/ai/storage.md capability matrix).
package azurebackend
