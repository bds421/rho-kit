// asvs: V12.1.1, V12.3.1, V13.4.1
package storagehttp

import (
	"context"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/bds421/rho-kit/core/v2/redact"
	"github.com/bds421/rho-kit/infra/v2/storage"
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
	if ctx == nil {
		return UploadResult{}, fmt.Errorf("storagehttp: context is required")
	}
	if r == nil {
		return UploadResult{}, fmt.Errorf("storagehttp: request is required")
	}
	if backend == nil {
		return UploadResult{}, fmt.Errorf("storagehttp: backend is required")
	}
	if opts.KeyFunc == nil {
		return UploadResult{}, fmt.Errorf("storagehttp: KeyFunc is required (use UUIDKeyFunc for unique keys)")
	}
	if opts.MaxFileSize == 0 {
		return UploadResult{}, fmt.Errorf("storagehttp: MaxFileSize is required — set a positive byte cap, or pass Unlimited to opt out explicitly")
	}
	if opts.MaxFileSize < 0 && opts.MaxFileSize != Unlimited {
		return UploadResult{}, fmt.Errorf("storagehttp: MaxFileSize must be positive or Unlimited (-1)")
	}
	opts.applyDefaults()

	// Hard transport-level cap before the multipart parser sees a byte.
	// http.MaxBytesReader cuts the request stream at the configured
	// limit so a slow attacker streaming gigabytes of bogus data is
	// stopped at the wire, not after the multipart parser has read
	// past it. The cap is MaxFileSize plus a generous overhead for
	// headers + form metadata; cap at MaxFileSize+1MiB.
	if opts.MaxFileSize > 0 {
		const overhead = 1 << 20 // 1 MiB
		r.Body = http.MaxBytesReader(nil, r.Body, opts.MaxFileSize+overhead)
	}

	mr, err := r.MultipartReader()
	if err != nil {
		return UploadResult{}, storage.WrapSafe("storagehttp: parse multipart failed", err)
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
			return UploadResult{}, storage.WrapSafe("storagehttp: read multipart part failed", err)
		}

		if part.FormName() != opts.FormField {
			skipped++
			if skipped > maxSkippedParts {
				return UploadResult{}, fmt.Errorf("storagehttp: too many non-file parts")
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
				return UploadResult{}, fmt.Errorf("storagehttp: cumulative non-file part bytes exceed limit")
			}
			limit := int64(maxPartDiscard)
			if limit > remainingBudget {
				limit = remainingBudget
			}
			n, _ := io.Copy(io.Discard, io.LimitReader(part, limit+1))
			totalSkippedBytes += n
			if n > limit {
				if limit < maxPartDiscard {
					return UploadResult{}, fmt.Errorf("storagehttp: cumulative non-file part bytes exceed limit")
				}
				return UploadResult{}, fmt.Errorf("storagehttp: non-file part too large")
			}
			continue
		}

		return storePart(ctx, part, r, backend, opts)
	}

	return UploadResult{}, fmt.Errorf("storagehttp: no file part found in request")
}

func storePart(ctx context.Context, part *multipart.Part, r *http.Request, backend storage.Storage, opts UploadOptions) (UploadResult, error) {
	filename := part.FileName()
	if filename == "" {
		return UploadResult{}, fmt.Errorf("storagehttp: file part has no filename")
	}

	contentType, err := partContentType(part)
	if err != nil {
		return UploadResult{}, err
	}

	meta := storage.ObjectMeta{
		ContentType: contentType,
	}

	// Apply upload-level validators before key derivation so that
	// KeyFunc can use the sniffed ContentType (set by AllowedMIMETypes).
	// Prepend the mandatory MaxFileSize cap unless the caller explicitly
	// opted out with Unlimited — without this, an unconfigured Validators
	// slice would let a request stream unbounded bytes into backend.Put.
	validators := append([]storage.Validator(nil), opts.Validators...)
	if opts.MaxFileSize > 0 {
		validators = append([]storage.Validator{storage.MaxFileSize(opts.MaxFileSize)}, validators...)
	}
	var reader io.Reader = part
	if len(validators) > 0 {
		validated, err := storage.ApplyValidators(ctx, reader, &meta, validators)
		if err != nil {
			return UploadResult{}, err
		}
		reader = validated
		defer func() { _ = storage.CloseValidatedReader(validated) }()
	}

	if err := storage.ValidateObjectMeta(meta); err != nil {
		return UploadResult{}, err
	}

	key, err := deriveKey(opts.KeyFunc, r, filename, meta)
	if err != nil {
		return UploadResult{}, storage.WrapSafe("storagehttp: key derivation failed", err)
	}

	if err := storage.ValidateKey(key); err != nil {
		return UploadResult{}, fmt.Errorf("storagehttp: invalid derived key: %w", err)
	}

	cr := &countingReader{r: reader}
	if err := backend.Put(ctx, key, cr, meta); err != nil {
		return UploadResult{}, storage.WrapSafe("storagehttp: store failed", err)
	}

	return UploadResult{
		Key:         key,
		ContentType: meta.ContentType,
		Size:        cr.n,
	}, nil
}

func partContentType(part *multipart.Part) (string, error) {
	values := part.Header.Values("Content-Type")
	if len(values) == 0 {
		return "", nil
	}
	if len(values) != 1 {
		return "", fmt.Errorf("storagehttp: file part has multiple Content-Type headers")
	}
	raw := values[0]
	if !utf8.ValidString(raw) || strings.ContainsAny(raw, "\x00\r\n") {
		return "", fmt.Errorf("storagehttp: file part Content-Type contains invalid characters")
	}
	contentType := strings.TrimSpace(raw)
	if contentType == "" {
		return "", nil
	}
	if err := storage.ValidateObjectMeta(storage.ObjectMeta{ContentType: contentType}); err != nil {
		return "", err
	}
	return contentType, nil
}

func deriveKey(fn func(*http.Request, string, storage.ObjectMeta) (string, error), r *http.Request, filename string, meta storage.ObjectMeta) (key string, err error) {
	defer func() {
		if rec := recover(); rec != nil {
			key, err = "", fmt.Errorf("panic: %s", redact.PanicValue(rec))
		}
	}()
	return fn(r, filename, meta)
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
