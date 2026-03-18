package storagehttp

import (
	"context"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"

	"github.com/bds421/rho-kit/infra/storage"
)

// UploadResult holds metadata about a completed upload.
type UploadResult struct {
	// Key is the storage key where the file was stored.
	Key string

	// ContentType is the detected or declared MIME type of the uploaded file.
	ContentType string

	// Size is the number of bytes written, if known. Zero if unknown.
	Size int64
}

// UploadOptions configures [ParseAndStore] behavior.
type UploadOptions struct {
	// MaxMemory is the multipart memory buffer. Files exceeding this are
	// spooled to temp files by Go's stdlib. Defaults to 32 KiB — keep low
	// because we stream to storage rather than buffering.
	MaxMemory int64

	// FormField is the name of the file field in the multipart form.
	// Defaults to "file".
	FormField string

	// KeyFunc derives the storage key from the request, original filename,
	// and validated metadata. Validators run before KeyFunc, so
	// meta.ContentType reflects the sniffed MIME type (if [storage.AllowedMIMETypes]
	// is in the validator chain).
	//
	// This field is required — using raw client filenames as storage keys is
	// a security risk (path traversal, collisions). Use [UUIDKeyFunc] for a
	// sensible default.
	KeyFunc func(r *http.Request, filename string, meta storage.ObjectMeta) (string, error)

	// Validators are applied to the upload stream before storing.
	// These run in addition to any validators configured on the backend.
	Validators []storage.Validator
}

func (o *UploadOptions) applyDefaults() {
	if o.MaxMemory <= 0 {
		o.MaxMemory = 32 * 1024
	}
	if o.FormField == "" {
		o.FormField = "file"
	}
}

// ParseAndStore parses a multipart/form-data request and streams the file
// part directly to the storage backend. The file is never fully buffered in
// memory — the multipart reader pipes bytes straight into backend.Put.
//
// Returns UploadResult on success, or an error that may wrap
// [storage.ErrValidation] (use errors.Is to distinguish 422 vs 500 responses).
func ParseAndStore(ctx context.Context, r *http.Request, backend storage.Storage, opts UploadOptions) (UploadResult, error) {
	if opts.KeyFunc == nil {
		return UploadResult{}, fmt.Errorf("storagehttp: KeyFunc is required (use UUIDKeyFunc for unique keys)")
	}
	opts.applyDefaults()

	mr, err := r.MultipartReader()
	if err != nil {
		return UploadResult{}, fmt.Errorf("storagehttp: parse multipart: %w", err)
	}

	return processMultipartParts(ctx, mr, r, backend, opts)
}

// maxSkippedParts limits the number of non-target multipart parts we consume
// before giving up. Prevents DoS from requests with thousands of junk fields.
const maxSkippedParts = 10

func processMultipartParts(ctx context.Context, mr *multipart.Reader, r *http.Request, backend storage.Storage, opts UploadOptions) (UploadResult, error) {
	skipped := 0
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return UploadResult{}, fmt.Errorf("storagehttp: read multipart part: %w", err)
		}

		if part.FormName() != opts.FormField {
			skipped++
			if skipped > maxSkippedParts {
				return UploadResult{}, fmt.Errorf("storagehttp: too many non-file parts (limit %d)", maxSkippedParts)
			}
			// Discard up to 10 MiB. If the part exceeds this limit, reject
			// the request rather than leaving unread bytes that would corrupt
			// the multipart parse stream for subsequent parts.
			const maxPartDiscard = 10 << 20
			n, _ := io.Copy(io.Discard, io.LimitReader(part, maxPartDiscard+1))
			if n > maxPartDiscard {
				return UploadResult{}, fmt.Errorf("storagehttp: non-file part too large (limit %d bytes)", maxPartDiscard)
			}
			continue
		}

		return storePart(ctx, part, r, backend, opts)
	}

	return UploadResult{}, fmt.Errorf("storagehttp: no file part %q found in request", opts.FormField)
}

func storePart(ctx context.Context, part *multipart.Part, r *http.Request, backend storage.Storage, opts UploadOptions) (UploadResult, error) {
	filename := part.FileName()
	if filename == "" {
		return UploadResult{}, fmt.Errorf("storagehttp: file part has no filename")
	}

	meta := storage.ObjectMeta{
		ContentType: part.Header.Get("Content-Type"),
	}

	// Apply upload-level validators before key derivation so that
	// KeyFunc can use the sniffed ContentType (set by AllowedMIMETypes).
	var reader io.Reader = part
	if len(opts.Validators) > 0 {
		validated, err := storage.ApplyValidators(reader, &meta, opts.Validators)
		if err != nil {
			return UploadResult{}, err
		}
		reader = validated
	}

	key, err := opts.KeyFunc(r, filename, meta)
	if err != nil {
		return UploadResult{}, fmt.Errorf("storagehttp: key derivation: %w", err)
	}

	if err := storage.ValidateKey(key); err != nil {
		return UploadResult{}, fmt.Errorf("storagehttp: invalid derived key: %w", err)
	}

	cr := &countingReader{r: reader}
	if err := backend.Put(ctx, key, cr, meta); err != nil {
		return UploadResult{}, err
	}

	return UploadResult{
		Key:         key,
		ContentType: meta.ContentType,
		Size:        cr.n,
	}, nil
}

// countingReader wraps a reader and counts bytes read through it.
type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}
