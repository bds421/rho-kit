// Package uploadsec provides composable validators for HTTP file
// uploads. Use these alongside storagehttp's upload pipeline to reject
// content the server should never accept:
//
//   - [AllowMIMETypes] sniffs the actual bytes and rejects anything
//     outside the allowlist. Defends against the
//     "Content-Type: image/png; payload: PHP webshell" class.
//   - [AllowExtensions] gates on the filename extension. Use
//     alongside MIME sniffing — the two checks together force the
//     uploader to be honest in both directions.
//   - [MaxImageDimensions] reads only the image header and rejects
//     pixel counts above the configured cap. Defends against
//     decompression-bomb DoS where a 1 KB compressed image expands
//     to 100,000 × 100,000 RGBA in RAM.
//
// Validators compose via [Chain]: each runs in order, the first
// rejection wins. They share a [Meta] type so each step can refine
// what the next sees (sniffed Content-Type replaces the
// caller-supplied one, ImageWidth/Height become available after
// MaxImageDimensions).
package uploadsec

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	_ "image/gif"  // register decoder
	_ "image/jpeg" // register decoder
	_ "image/png"  // register decoder
	"io"
	"mime"
	"net/http"
	"path"
	"strings"
)

// Sentinel errors. Validators return these (or values that wrap them)
// so the upload handler can map them to 4xx HTTP responses.
var (
	ErrMIMETypeNotAllowed  = errors.New("uploadsec: MIME type not allowed")
	ErrExtensionNotAllowed = errors.New("uploadsec: file extension not allowed")
	ErrImageTooLarge       = errors.New("uploadsec: image dimensions exceed limit")
	ErrInvalidImage        = errors.New("uploadsec: image header could not be parsed")
)

// Meta is the upload context exchanged between validators. Validators
// may return an updated Meta to override the caller-supplied
// ContentType (after MIME sniffing), or to attach derived metadata
// (image dimensions).
type Meta struct {
	Filename    string
	ContentType string
	Size        int64
	ImageWidth  int
	ImageHeight int
}

// Validator inspects the body and metadata and returns either an
// updated Meta (allow) or a non-nil error (reject). The body is
// rewound after each validator runs, so each step sees the full
// content from offset 0.
type Validator interface {
	Validate(ctx context.Context, body io.ReadSeeker, meta Meta) (Meta, error)
}

// ValidatorFunc adapts a function to Validator.
type ValidatorFunc func(ctx context.Context, body io.ReadSeeker, meta Meta) (Meta, error)

// Validate implements Validator.
func (f ValidatorFunc) Validate(ctx context.Context, body io.ReadSeeker, meta Meta) (Meta, error) {
	return f(ctx, body, meta)
}

// Chain runs validators in order. Each receives the Meta produced by
// the previous step. The first error short-circuits the chain.
func Chain(validators ...Validator) Validator {
	return ValidatorFunc(func(ctx context.Context, body io.ReadSeeker, meta Meta) (Meta, error) {
		for _, v := range validators {
			if _, err := body.Seek(0, io.SeekStart); err != nil {
				return meta, fmt.Errorf("uploadsec: rewind body: %w", err)
			}
			updated, err := v.Validate(ctx, body, meta)
			if err != nil {
				return meta, err
			}
			meta = updated
		}
		// Final rewind so the caller's persistence step sees the body.
		_, err := body.Seek(0, io.SeekStart)
		return meta, err
	})
}

// AllowMIMETypes returns a Validator that sniffs the first 512 bytes
// of the body and rejects content whose detected MIME type is not in
// the allowlist. The detected type replaces meta.ContentType so
// downstream steps and storage backends record the truth, not the
// caller-supplied lie.
//
// Sniffing uses [http.DetectContentType] (RFC 2046 + Mozilla heuristics),
// which is the same logic stdlib uses for static-file serving. It is
// not exhaustive — exotic formats may be detected as
// application/octet-stream. Rely on AllowExtensions for those edge
// cases or extend the allowlist with the kit's own MIME registry.
func AllowMIMETypes(allowed ...string) Validator {
	allowSet := make(map[string]struct{}, len(allowed))
	for _, m := range allowed {
		allowSet[strings.ToLower(strings.TrimSpace(m))] = struct{}{}
	}
	return ValidatorFunc(func(_ context.Context, body io.ReadSeeker, meta Meta) (Meta, error) {
		buf := make([]byte, 512)
		n, err := io.ReadFull(body, buf)
		if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
			return meta, fmt.Errorf("uploadsec: sniff body: %w", err)
		}
		sniffed := http.DetectContentType(buf[:n])
		// DetectContentType returns "type; charset=…" for text; strip params for the allowlist match.
		base, _, _ := strings.Cut(sniffed, ";")
		base = strings.ToLower(strings.TrimSpace(base))
		if _, ok := allowSet[base]; !ok {
			return meta, fmt.Errorf("%w: %q", ErrMIMETypeNotAllowed, base)
		}
		meta.ContentType = base
		return meta, nil
	})
}

// AllowExtensions returns a Validator that rejects files whose
// filename extension is not in the allowlist. Extensions are matched
// case-insensitively and must include the leading dot ("." prefix).
// Filenames without an extension are rejected.
func AllowExtensions(allowed ...string) Validator {
	allowSet := make(map[string]struct{}, len(allowed))
	for _, e := range allowed {
		allowSet[strings.ToLower(strings.TrimSpace(e))] = struct{}{}
	}
	return ValidatorFunc(func(_ context.Context, _ io.ReadSeeker, meta Meta) (Meta, error) {
		ext := strings.ToLower(path.Ext(meta.Filename))
		if ext == "" {
			return meta, fmt.Errorf("%w: filename %q has no extension", ErrExtensionNotAllowed, meta.Filename)
		}
		if _, ok := allowSet[ext]; !ok {
			return meta, fmt.Errorf("%w: %q", ErrExtensionNotAllowed, ext)
		}
		// Cross-check: the configured extension should match the canonical
		// extension for the (sniffed) content type, when both are known.
		// This catches "foo.png with image/jpeg bytes" inconsistencies.
		if meta.ContentType != "" {
			if exts, _ := mime.ExtensionsByType(meta.ContentType); len(exts) > 0 {
				ok := false
				for _, e := range exts {
					if strings.EqualFold(e, ext) {
						ok = true
						break
					}
				}
				if !ok {
					return meta, fmt.Errorf("%w: extension %q does not match content type %q", ErrExtensionNotAllowed, ext, meta.ContentType)
				}
			}
		}
		return meta, nil
	})
}

// imageHeaderReadLimit caps the bytes read for image.DecodeConfig.
// Standard image headers (PNG, JPEG, GIF) parse within the first ~1 KiB;
// 64 KiB is generous for exotic format variants while keeping memory use
// bounded regardless of upload size. Reading the full body would let a
// 100 MiB upload buffer 100 MiB in RAM before any size check runs.
const imageHeaderReadLimit = 64 << 10

// MaxImageDimensions returns a Validator that rejects images whose
// width × height exceeds maxWidth × maxHeight. Only the image header
// is parsed (image.DecodeConfig); the full pixel buffer is never
// allocated, so a 100,000 × 100,000 PNG decompression bomb is rejected
// without ever materialising the megabytes of pixels.
//
// Memory use is bounded by [imageHeaderReadLimit] (64 KiB) regardless of
// the upload size — only the header is read into memory, the body is left
// at its current offset for downstream validators / persistence.
//
// Non-image content types pass through unchanged. Wire AllowMIMETypes
// before this validator so meta.ContentType is the sniffed value.
func MaxImageDimensions(maxWidth, maxHeight int) Validator {
	if maxWidth <= 0 || maxHeight <= 0 {
		panic("uploadsec: maxWidth and maxHeight must be positive")
	}
	return ValidatorFunc(func(_ context.Context, body io.ReadSeeker, meta Meta) (Meta, error) {
		if !strings.HasPrefix(meta.ContentType, "image/") {
			return meta, nil
		}
		// Read only enough bytes for image.DecodeConfig. Using a bounded
		// LimitReader instead of ReadAll caps validator memory at
		// imageHeaderReadLimit even for arbitrarily large uploads.
		header, err := io.ReadAll(io.LimitReader(body, imageHeaderReadLimit))
		if err != nil {
			return meta, fmt.Errorf("uploadsec: buffer image header: %w", err)
		}
		cfg, _, err := image.DecodeConfig(bytes.NewReader(header))
		if err != nil {
			return meta, fmt.Errorf("%w: %v", ErrInvalidImage, err)
		}
		if cfg.Width > maxWidth || cfg.Height > maxHeight {
			return meta, fmt.Errorf("%w: %dx%d exceeds %dx%d",
				ErrImageTooLarge, cfg.Width, cfg.Height, maxWidth, maxHeight)
		}
		meta.ImageWidth = cfg.Width
		meta.ImageHeight = cfg.Height
		return meta, nil
	})
}

// HTTPStatusForError maps a uploadsec sentinel error to an HTTP
// status. Callers wiring uploadsec into their own handler can use
// this to keep the response codes consistent across the kit:
//
//   - 415 Unsupported Media Type: ErrMIMETypeNotAllowed
//   - 422 Unprocessable Entity: ErrExtensionNotAllowed,
//     ErrImageTooLarge, ErrInvalidImage
//   - 500 Internal Server Error: anything else
func HTTPStatusForError(err error) int {
	switch {
	case errors.Is(err, ErrMIMETypeNotAllowed):
		return http.StatusUnsupportedMediaType
	case errors.Is(err, ErrExtensionNotAllowed),
		errors.Is(err, ErrImageTooLarge),
		errors.Is(err, ErrInvalidImage):
		return http.StatusUnprocessableEntity
	default:
		return http.StatusInternalServerError
	}
}
