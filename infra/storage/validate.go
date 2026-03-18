package storage

import (
	"bytes"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"strings"

	"github.com/gabriel-vasile/mimetype"
)

// mimeSniffSize is the number of bytes read for MIME type detection.
// 3072 bytes is sufficient for mimetype library to detect most formats.
const mimeSniffSize = 3072

// Validator inspects a reader before it reaches the storage backend.
// It may wrap the reader (e.g. to enforce size limits) or return an
// error wrapping ErrValidation to reject the upload.
//
// Implementations must not buffer the entire stream into memory.
// The returned io.Reader replaces the input for the next validator in the chain.
// Validators may modify meta (e.g. to set ContentType after sniffing).
type Validator func(r io.Reader, meta *ObjectMeta) (io.Reader, error)

// AllowedMIMETypes returns a Validator that detects the actual MIME type
// by sniffing the first bytes of content (not the file extension or
// declared Content-Type). The sniffed bytes are prepended back to the
// reader so the full content is available to the backend.
//
// The detected type overwrites meta.ContentType. If the detected type
// is not in the allowed set, returns an error wrapping ErrValidation.
func AllowedMIMETypes(allowed ...string) Validator {
	exact := make(map[string]struct{}, len(allowed))
	var wildcards []string // e.g. "image/*" → prefix "image/"
	for _, t := range allowed {
		if strings.HasSuffix(t, "/*") {
			wildcards = append(wildcards, strings.TrimSuffix(t, "*"))
		} else {
			exact[t] = struct{}{}
		}
	}

	return func(r io.Reader, meta *ObjectMeta) (io.Reader, error) {
		header := make([]byte, mimeSniffSize)
		n, err := io.ReadFull(r, header)
		if err != nil && err != io.ErrUnexpectedEOF {
			return nil, fmt.Errorf("storage: read for MIME detection: %w", err)
		}
		header = header[:n]

		detected := mimetype.Detect(header)
		meta.ContentType = detected.String()

		if _, ok := exact[detected.String()]; ok {
			return io.MultiReader(bytes.NewReader(header), r), nil
		}
		for _, prefix := range wildcards {
			if strings.HasPrefix(detected.String(), prefix) {
				return io.MultiReader(bytes.NewReader(header), r), nil
			}
		}

		return nil, fmt.Errorf("%w: MIME type %q is not allowed", ErrValidation, detected.String())
	}
}

// MaxFileSize returns a Validator that enforces a maximum content size.
// If meta.Size is set and already exceeds maxBytes, the error is returned
// immediately without reading any content. Otherwise, a limit-enforcing
// reader wraps the stream so the limit is checked during consumption.
func MaxFileSize(maxBytes int64) Validator {
	return func(r io.Reader, meta *ObjectMeta) (io.Reader, error) {
		if meta.Size > 0 && meta.Size > maxBytes {
			return nil, fmt.Errorf("%w: declared size %d exceeds max %d bytes",
				ErrValidation, meta.Size, maxBytes)
		}

		return &limitReader{r: r, remaining: maxBytes, max: maxBytes}, nil
	}
}

// limitReader wraps io.Reader and returns ErrValidation when maxBytes is exceeded.
type limitReader struct {
	r         io.Reader
	remaining int64
	max       int64
	overflow  bool
}

func (lr *limitReader) Read(p []byte) (int, error) {
	if lr.overflow {
		return 0, fmt.Errorf("%w: content exceeds max %d bytes", ErrValidation, lr.max)
	}

	// Cap the read to remaining+1 so we can detect overflow without
	// returning excess bytes to the caller.
	if int64(len(p)) > lr.remaining+1 {
		p = p[:lr.remaining+1]
	}

	n, err := lr.r.Read(p)
	lr.remaining -= int64(n)

	if lr.remaining < 0 {
		lr.overflow = true
		// Return only the bytes that fit within the limit. lr.remaining is
		// negative, so n + remaining trims the overflow portion.
		valid := n + int(lr.remaining)
		if valid < 0 {
			valid = 0
		}
		return valid, fmt.Errorf("%w: content exceeds max %d bytes", ErrValidation, lr.max)
	}

	return n, err
}

// imageDimensionReadLimit caps how many bytes ImageDimensions will buffer
// while decoding the image header. image.DecodeConfig typically reads < 1 KiB
// for standard formats; this limit prevents memory exhaustion from malformed input.
const imageDimensionReadLimit = 512 << 10 // 512 KiB — image headers are typically < 1 KiB

// ImageDimensions returns a Validator that checks image width and height
// by decoding only the image header (Go's image.DecodeConfig reads minimally).
// The consumed bytes are prepended back to the reader via io.MultiReader.
//
// A maxW or maxH of 0 means no upper limit for that dimension.
// Returns an error wrapping ErrValidation if dimensions fall outside bounds.
func ImageDimensions(minW, minH, maxW, maxH int) Validator {
	return func(r io.Reader, meta *ObjectMeta) (io.Reader, error) {
		var buf bytes.Buffer
		lr := io.LimitReader(r, imageDimensionReadLimit)
		tr := io.TeeReader(lr, &buf)

		cfg, _, err := image.DecodeConfig(tr)
		if err != nil {
			return nil, fmt.Errorf("%w: cannot decode image dimensions: %v", ErrValidation, err)
		}

		if cfg.Width < minW || cfg.Height < minH {
			return nil, fmt.Errorf("%w: image %dx%d is smaller than minimum %dx%d",
				ErrValidation, cfg.Width, cfg.Height, minW, minH)
		}
		if maxW > 0 && cfg.Width > maxW {
			return nil, fmt.Errorf("%w: image width %d exceeds maximum %d",
				ErrValidation, cfg.Width, maxW)
		}
		if maxH > 0 && cfg.Height > maxH {
			return nil, fmt.Errorf("%w: image height %d exceeds maximum %d",
				ErrValidation, cfg.Height, maxH)
		}

		return io.MultiReader(&buf, r), nil
	}
}

// ApplyValidators runs validators in sequence on the given reader.
// If any validator returns an error, the chain stops immediately.
// The returned reader is the validated, potentially-wrapped stream
// ready for the backend.
func ApplyValidators(r io.Reader, meta *ObjectMeta, validators []Validator) (io.Reader, error) {
	for _, v := range validators {
		var err error
		r, err = v(r, meta)
		if err != nil {
			return nil, err
		}
	}
	return r, nil
}
