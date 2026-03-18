package storage

import (
	"context"
	"io"
)

// MultipartUploader is an optional interface for backends that support
// chunked uploads of large files (e.g. S3 multipart upload).
// Check capability via type assertion:
//
//	if mu, ok := backend.(storage.MultipartUploader); ok {
//	    upload, err := mu.InitUpload(ctx, key, meta)
//	    // ... upload parts ...
//	    err = mu.CompleteUpload(ctx, upload)
//	}
type MultipartUploader interface {
	// InitUpload starts a new multipart upload session and returns an upload ID.
	InitUpload(ctx context.Context, key string, meta ObjectMeta) (MultipartUpload, error)

	// UploadPart uploads a single part. partNumber starts at 1.
	// Returns a PartInfo that must be passed to CompleteUpload.
	UploadPart(ctx context.Context, upload MultipartUpload, partNumber int, r io.Reader) (PartInfo, error)

	// CompleteUpload assembles all uploaded parts into the final object.
	CompleteUpload(ctx context.Context, upload MultipartUpload, parts []PartInfo) error

	// AbortUpload cancels a multipart upload and cleans up any uploaded parts.
	AbortUpload(ctx context.Context, upload MultipartUpload) error
}

// MultipartUpload identifies an in-progress multipart upload session.
type MultipartUpload struct {
	// Key is the storage key for the final object.
	Key string

	// UploadID is the backend-specific identifier for this upload session.
	UploadID string
}

// PartInfo describes a successfully uploaded part.
type PartInfo struct {
	// PartNumber is the 1-based part index.
	PartNumber int

	// ETag is the backend-specific identifier for this part (e.g. S3 ETag).
	ETag string

	// Size is the number of bytes in this part.
	Size int64
}
