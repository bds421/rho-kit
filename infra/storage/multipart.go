package storage

import (
	"context"
	"fmt"
	"io"
	"time"
)

// MultipartUploader is an optional interface for backends that support
// chunked uploads of large files (e.g. S3 multipart upload).
// Check capability via [AsMultipartUploader] so decorators with [Unwrapper]
// support are handled consistently:
//
//	if mu, ok := storage.AsMultipartUploader(backend); ok {
//	    upload, err := mu.InitUpload(ctx, key, meta)
//	    // ... upload parts ...
//	    err = mu.CompleteUpload(ctx, upload, parts)
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

	// ChecksumSHA256 is the base64-encoded SHA-256 returned by the backend.
	// Portable callers require it for every part and pass it unchanged to
	// CompleteUpload; an ETag is not a content checksum.
	ChecksumSHA256 string
}

// MultipartUploadInfo describes one still-active upload. It is intentionally
// separate from object listing: incomplete uploads are not visible objects.
type MultipartUploadInfo struct {
	Upload      MultipartUpload
	InitiatedAt time.Time
}

const MaxMultipartUploadPageSize = 1000

// MultipartUploadListOptions bounds and pages active-upload maintenance.
// InitiatedBefore, when set, must be UTC and excludes uploads initiated at or
// after the cutoff.
type MultipartUploadListOptions struct {
	MaxUploads      int
	KeyMarker       string
	UploadIDMarker  string
	InitiatedBefore time.Time
}

type MultipartUploadPage struct {
	Uploads            []MultipartUploadInfo
	NextKeyMarker      string
	NextUploadIDMarker string
	Truncated          bool
}

// MultipartUploadLister is an optional maintenance capability for bounded
// stale-upload collection. It lists active sessions, never completed objects.
type MultipartUploadLister interface {
	ListMultipartUploads(context.Context, string, MultipartUploadListOptions) (MultipartUploadPage, error)
}

func ValidateMultipartUploadListOptions(opts MultipartUploadListOptions) error {
	if opts.MaxUploads <= 0 {
		return fmt.Errorf("%w: multipart upload page size must be positive", ErrValidation)
	}
	if opts.MaxUploads > MaxMultipartUploadPageSize {
		return fmt.Errorf("%w: multipart upload page size exceeds %d", ErrValidation, MaxMultipartUploadPageSize)
	}
	if err := ValidateKeyMarker(opts.KeyMarker); err != nil {
		return err
	}
	if opts.UploadIDMarker != "" && opts.KeyMarker == "" {
		return fmt.Errorf("%w: multipart upload id marker requires a key marker", ErrValidation)
	}
	if len(opts.UploadIDMarker) > MaxKeyLen || containsInvalidKeyRune(opts.UploadIDMarker) {
		return fmt.Errorf("%w: multipart upload id marker is invalid", ErrValidation)
	}
	if !opts.InitiatedBefore.IsZero() && opts.InitiatedBefore.Location() != time.UTC {
		return fmt.Errorf("%w: multipart upload cutoff must use UTC", ErrValidation)
	}
	return nil
}

func ValidateKeyMarker(marker string) error {
	if marker == "" {
		return nil
	}
	return ValidateKey(marker)
}
