// Package storagehttp provides HTTP helpers for streaming multipart file
// uploads directly to a [storage.Storage] backend.
//
// The key function is [ParseAndStore], which reads a multipart/form-data
// request and pipes the file part directly to the backend without buffering
// the entire file in memory.
package storagehttp
