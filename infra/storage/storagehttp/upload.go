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
	// MaxMemory was previously documented as "the multipart memory buffer"
	// but [ParseAndStore] uses [http.Request.MultipartReader] which streams
	// parts directly without honouring this field. Kept for backwards-
	// compatible doc-references; reads are silently ignored.
	//
	// Deprecated: ignored. The streaming reader path makes the field a
	// misleading no-op. Will be removed in v2.
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
	// A [storage.MaxFileSize] validator equal to MaxFileSize is prepended
	// automatically to enforce the cap.
	Validators []storage.Validator

	// MaxTotalSkippedBytes caps the cumulative size of multipart parts that
	// don't match FormField. Without it, ParseAndStore could discard up to
	// maxPartDiscard × maxSkippedParts (~100 MiB) of attacker-supplied
	// data per request. Default: 16 MiB.
	MaxTotalSkippedBytes int64

	// MaxFileSize is the maximum number of bytes ParseAndStore will accept
	// for the file part. REQUIRED — without an explicit cap a malicious
	// client could stream gigabytes into the backend. Use [Unlimited] to
	// opt out explicitly when an upstream layer (reverse proxy, body
	// limiter middleware) already enforces a cap.
	MaxFileSize int64
}

// Unlimited disables the [UploadOptions.MaxFileSize] cap. Pass this only
// when an upstream layer (reverse proxy, body limiter middleware) already
// caps request bodies — otherwise a single Put can stream unbounded bytes.
const Unlimited int64 = -1

func (o *UploadOptions) applyDefaults() {
	if o.FormField == "" {
		o.FormField = "file"
	}
	if o.MaxTotalSkippedBytes <= 0 {
		o.MaxTotalSkippedBytes = 16 << 20
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
	if opts.MaxFileSize == 0 {
		return UploadResult{}, fmt.Errorf("storagehttp: MaxFileSize is required — set a positive byte cap, or pass Unlimited to opt out explicitly")
	}
	if opts.MaxFileSize < 0 && opts.MaxFileSize != Unlimited {
		return UploadResult{}, fmt.Errorf("storagehttp: MaxFileSize must be positive or Unlimited (-1), got %d", opts.MaxFileSize)
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
	var totalSkippedBytes int64
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
			// Discard up to 10 MiB per part. If a single part exceeds the
			// per-part limit OR the cumulative budget across all skipped
			// parts exceeds opts.MaxTotalSkippedBytes, reject the request.
			// The cumulative cap defends against an attacker fanning ~10 MiB
			// across the maxSkippedParts × per-part budget (~100 MiB) of
			// pure waste per request.
			const maxPartDiscard = 10 << 20
			remainingBudget := opts.MaxTotalSkippedBytes - totalSkippedBytes
			if remainingBudget <= 0 {
				return UploadResult{}, fmt.Errorf("storagehttp: cumulative non-file part bytes exceed limit (%d)", opts.MaxTotalSkippedBytes)
			}
			limit := int64(maxPartDiscard)
			if limit > remainingBudget {
				limit = remainingBudget
			}
			n, _ := io.Copy(io.Discard, io.LimitReader(part, limit+1))
			totalSkippedBytes += n
			if n > limit {
				if limit < maxPartDiscard {
					return UploadResult{}, fmt.Errorf("storagehttp: cumulative non-file part bytes exceed limit (%d)", opts.MaxTotalSkippedBytes)
				}
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
	// Prepend the mandatory MaxFileSize cap unless the caller explicitly
	// opted out with Unlimited — without this, an unconfigured Validators
	// slice would let a request stream unbounded bytes into backend.Put.
	validators := opts.Validators
	if opts.MaxFileSize > 0 {
		validators = append([]storage.Validator{storage.MaxFileSize(opts.MaxFileSize)}, validators...)
	}
	var reader io.Reader = part
	if len(validators) > 0 {
		validated, err := storage.ApplyValidators(reader, &meta, validators)
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
