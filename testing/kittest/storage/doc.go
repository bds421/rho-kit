// Package storage re-exports the storage test helpers from
// infra/storage/storagetest: the local-backend helper and the shared
// compliance suites are available without build tags; the testcontainer
// helpers (StartS3, StartSFTP) are re-exported under the `integration`
// build tag.
package storage
