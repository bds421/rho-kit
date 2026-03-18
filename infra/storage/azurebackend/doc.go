// Package azurebackend provides an Azure Blob Storage implementation of
// [storage.Storage].
//
// It uses the official Azure SDK for Go (azblob) and supports Azure
// Storage accounts, Azure Government, and Azurite for local development.
//
// All operations are instrumented with OpenTelemetry traces.
package azurebackend
