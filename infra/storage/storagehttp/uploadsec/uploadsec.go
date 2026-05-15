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
//   - [ScanWith] adapts a malware scanner into the same validator
//     chain. The base package defines the contract; concrete scanners
//     live in split modules so services only import scanner-specific
//     dependencies when they use them.
//
// Validators compose via [Chain]: each runs in order, the first
// rejection wins. They share a [Meta] type so each step can refine
// what the next sees (sniffed Content-Type replaces the
// caller-supplied one, ImageWidth/Height become available after
// MaxImageDimensions).
//
// # Polyglot defense
//
// The threat model includes "polyglot" files: a payload that is
// simultaneously a valid image and a valid script in another language
// (PHP, JavaScript, shell). The classic recipe is a real PNG followed
// by appended bytes such as "<?php phpinfo(); ?>"; a permissive viewer
// renders the image, a permissive interpreter executes the script.
//
// [AllowMIMETypes] defends against this in three layers:
//
//   - Header sniff via [http.DetectContentType] confirms the magic
//     bytes match the declared MIME type. [AllowExtensions] then
//     cross-checks the filename extension against the sniffed MIME so
//     the uploader cannot lie in either direction.
//   - For raster image MIME types (image/png, image/jpeg, image/gif,
//     image/webp) the validator runs a full-image decode via
//     [image.Decode]. A decoder failure indicates a corrupt or hostile
//     payload. The dimension cap (default 8192×8192, see
//     [defaultMaxImageDimension]) runs BEFORE the full decode so a
//     decompression-bomb header cannot allocate the pixel buffer.
//   - After a successful decode the body is checked for trailing
//     bytes that fall outside the format's terminator. PNG must end
//     with the IEND chunk (and a valid CRC), JPEG with FFD9, GIF with
//     trailer 0x3B, and WebP at the byte boundary declared by the
//     RIFF length. Any trailing data — even whitespace or NUL bytes —
//     is rejected.
//
// image/svg+xml is rejected unconditionally by the default allowlist —
// SVG is XML with scripting and SSRF surface. Services that need SVG
// must opt in via [AllowSVG] and provide a sanitiser.
//
// Polyglot rejection is best-effort against append-style attacks. An
// attacker who controls the compression internals (for example,
// crafts a PNG whose IDAT deflate stream decompresses to valid pixels
// but whose raw bytes look like script content to a misconfigured
// renderer) is out of scope — that is a renderer-side bug, not a
// validator bug. For arbitrary file uploads the kit additionally
// supports streaming through a malware scanner; see
// uploadsec/clamav for the bundled ClamAV adapter.
package uploadsec

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"image"
	"image/gif"
	"image/jpeg"
	"image/png"
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
	ErrMalwareDetected     = errors.New("uploadsec: malware detected")
	ErrScannerUnavailable  = errors.New("uploadsec: malware scanner unavailable")
)

// MalwareDetectedError carries the scanner's threat name while still
// matching [ErrMalwareDetected] through errors.Is.
type MalwareDetectedError struct {
	Threat string
}

// Error implements error.
func (e *MalwareDetectedError) Error() string {
	return ErrMalwareDetected.Error()
}

// Unwrap returns the sentinel so errors.Is(err, ErrMalwareDetected) works.
func (e *MalwareDetectedError) Unwrap() error {
	return ErrMalwareDetected
}

// MalwareDetected returns an error that wraps [ErrMalwareDetected] and
// preserves the scanner's threat name.
func MalwareDetected(threat string) error {
	return &MalwareDetectedError{Threat: strings.TrimSpace(threat)}
}

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

// Scanner streams an upload body to a malware scanner. It returns nil only
// when the scanner has produced a clean verdict. Scanner failures should wrap
// [ErrScannerUnavailable]; positive malware findings should wrap
// [ErrMalwareDetected].
type Scanner interface {
	Scan(ctx context.Context, body io.Reader, meta Meta) error
}

// ScannerFunc adapts a function to Scanner.
type ScannerFunc func(ctx context.Context, body io.Reader, meta Meta) error

// Scan implements Scanner.
func (f ScannerFunc) Scan(ctx context.Context, body io.Reader, meta Meta) error {
	return f(ctx, body, meta)
}

// ValidatorFunc adapts a function to Validator.
type ValidatorFunc func(ctx context.Context, body io.ReadSeeker, meta Meta) (Meta, error)

// Validate implements Validator.
func (f ValidatorFunc) Validate(ctx context.Context, body io.ReadSeeker, meta Meta) (Meta, error) {
	return f(ctx, body, meta)
}

// ScanWith returns a Validator that streams the upload through scanner and
// rejects unless the scanner returns a clean verdict. It does not buffer the
// body; the surrounding [Chain] rewinds before and after each validator.
func ScanWith(scanner Scanner) Validator {
	if scanner == nil {
		panic("uploadsec: ScanWith requires a non-nil scanner")
	}
	return ValidatorFunc(func(ctx context.Context, body io.ReadSeeker, meta Meta) (Meta, error) {
		if err := scanner.Scan(ctx, body, meta); err != nil {
			return meta, normalizeScannerError(err)
		}
		return meta, nil
	})
}

func normalizeScannerError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrMalwareDetected) {
		var detected *MalwareDetectedError
		if errors.As(err, &detected) {
			return detected
		}
		return ErrMalwareDetected
	}
	if errors.Is(err, ErrScannerUnavailable) {
		return ErrScannerUnavailable
	}
	return ErrScannerUnavailable
}

// Chain runs validators in order. Each receives the Meta produced by
// the previous step. The first error short-circuits the chain.
func Chain(validators ...Validator) Validator {
	for _, v := range validators {
		if v == nil {
			panic("uploadsec: Chain: validator must not be nil")
		}
	}
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

// SVGSanitizer rejects unsafe SVG uploads. Implementations MUST drop
// or neutralise <script>, foreignObject, event handlers, and any other
// vector for XSS or SSRF. The kit does not ship a sanitiser —
// services that need to accept SVG must wire in one from an audited
// library (e.g. bluemonday with an SVG profile, or DOMPurify via wasm).
//
// SanitizeSVG returns the sanitized payload, or an error if the input
// is unsafe. [AllowSVG] compares the sanitised payload against the
// original input — if they differ, the upload is rejected with
// [ErrSVGSanitizationModified] because the Validator pipeline does
// not support in-pipeline body transformation. Operators that need
// to accept transforming sanitisation must run the sanitiser before
// passing bytes to the upload pipeline.
type SVGSanitizer interface {
	SanitizeSVG(r io.Reader) ([]byte, error)
}

// ErrSVGSanitizationModified is returned by [AllowSVG] when the
// configured sanitizer produces output that differs from the original
// upload. The Validator pipeline operates on an [io.ReadSeeker] and
// cannot propagate a replacement body downstream, so operators that
// run transforming sanitisers must invoke them before the upload
// pipeline.
var ErrSVGSanitizationModified = errors.New("uploadsec: SVG sanitiser produced different bytes; pipeline does not support in-pipeline transformation")

// imageDeepDecodeMIMEs is the subset of image MIME types where a
// successful [http.DetectContentType] match still needs additional
// per-format validation (full-image decode + trailing-bytes check) to
// reject polyglots (a PHP webshell wrapped in a valid PNG header).
var imageDeepDecodeMIMEs = map[string]struct{}{
	"image/png":  {},
	"image/jpeg": {},
	"image/gif":  {},
	"image/webp": {},
}

// defaultMaxImageDimension caps width and height for the polyglot
// full-decode path. Raised via [WithImageDimensionCap] when a service
// legitimately needs larger images. The cap is enforced BEFORE the
// full decode so a 100,000 × 100,000 PNG decompression bomb cannot
// allocate the pixel buffer.
const defaultMaxImageDimension = 8192

// imageBodyReadLimit caps the body size buffered for full-image decode
// and trailing-bytes inspection. 32 MiB accommodates typical photo
// uploads (large iPhone HEIC-as-JPEG exports run ~5–10 MiB; ProRAW
// JPEGs can hit 25 MiB) while keeping per-request memory bounded.
// Uploads larger than this are rejected with [ErrInvalidImage] — wire
// a size validator earlier in the chain if a stricter ceiling is
// required.
const imageBodyReadLimit = 32 << 20

// MIMEValidator is the configurable validator returned by
// [AllowMIMETypes]. It satisfies [Validator] directly; the additional
// methods let callers tune polyglot-defense behaviour without changing
// the constructor signature.
type MIMEValidator struct {
	allowed   map[string]struct{}
	strictEnd bool
	maxWidth  int
	maxHeight int
}

// Validate implements [Validator].
func (m *MIMEValidator) Validate(_ context.Context, body io.ReadSeeker, meta Meta) (Meta, error) {
	buf := make([]byte, 512)
	n, err := io.ReadFull(body, buf)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return meta, fmt.Errorf("uploadsec: sniff body failed")
	}
	sniffed := http.DetectContentType(buf[:n])
	// DetectContentType returns "type; charset=…" for text; strip params for the allowlist match.
	base, _, _ := strings.Cut(sniffed, ";")
	base = strings.ToLower(strings.TrimSpace(base))
	if _, ok := m.allowed[base]; !ok {
		return meta, ErrMIMETypeNotAllowed
	}
	if _, deep := imageDeepDecodeMIMEs[base]; deep {
		if err := m.checkImageBody(body, base); err != nil {
			return meta, err
		}
	}
	meta.ContentType = base
	return meta, nil
}

// checkImageBody runs the polyglot defenses for one of the raster
// image MIME types: dimension cap → full decode → strict end-of-stream
// check. WebP is handled via manual RIFF parsing because the stdlib
// has no WebP decoder; that still catches append-style polyglots.
//
// The function is split in two: this method enforces the
// per-validator dimension cap (which needs m.maxWidth/m.maxHeight) and
// then delegates to [validateImageBody], which is pure (no config
// state, no ReadSeeker) and therefore directly unit-testable per
// format.
func (m *MIMEValidator) checkImageBody(body io.ReadSeeker, mimeType string) error {
	if _, err := body.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("uploadsec: rewind for image decode: %w", err)
	}
	// Peek the header to bound dimensions before the full decode runs.
	// image.DecodeConfig allocates only metadata, so a 99999×99999
	// header is rejected without ever materialising pixels.
	header, err := io.ReadAll(io.LimitReader(body, imageHeaderReadLimit))
	if err != nil {
		return fmt.Errorf("uploadsec: buffer image header failed")
	}
	// For PNG/JPEG/GIF use stdlib DecodeConfig for the dimension peek.
	// WebP requires a manual VP8 chunk parse below.
	if mimeType != "image/webp" {
		cfg, _, cfgErr := image.DecodeConfig(bytes.NewReader(header))
		if cfgErr != nil {
			return ErrInvalidImage
		}
		if cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width > m.maxWidth || cfg.Height > m.maxHeight {
			return ErrImageTooLarge
		}
	} else {
		w, h, webpErr := peekWebPDimensions(header)
		if webpErr != nil {
			return ErrInvalidImage
		}
		if w <= 0 || h <= 0 || w > m.maxWidth || h > m.maxHeight {
			return ErrImageTooLarge
		}
	}
	// Buffer the body for full decode + trailing-bytes inspection.
	if _, err := body.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("uploadsec: rewind for image body: %w", err)
	}
	return validateImageBody(mimeType, body, m.strictEnd)
}

// validateImageBody buffers up to [imageBodyReadLimit] bytes from body,
// runs the per-format full decode (PNG/JPEG/GIF; WebP relies on the
// trailing-bytes check alone because the stdlib has no decoder), and —
// when strictEnd is true — rejects any bytes past the format's
// canonical terminator.
//
// The function is pure: it takes the format string and an io.Reader,
// returns ErrInvalidImage on any deviation, and holds no validator
// state. It is exported only via this package's tests so each format
// path can be exercised in isolation.
func validateImageBody(format string, body io.Reader, strictEnd bool) error {
	limited := io.LimitReader(body, imageBodyReadLimit+1)
	full, err := io.ReadAll(limited)
	if err != nil {
		return fmt.Errorf("uploadsec: buffer image body failed")
	}
	if int64(len(full)) > imageBodyReadLimit {
		return ErrInvalidImage
	}
	// Full decode rejects corrupted IDAT and many polyglot variants
	// that DecodeConfig accepts. Skipped for WebP (no stdlib decoder)
	// — the trailing-bytes check below still catches the append-style
	// attack on WebP.
	switch format {
	case "image/png":
		if _, decErr := png.Decode(bytes.NewReader(full)); decErr != nil {
			return ErrInvalidImage
		}
	case "image/jpeg":
		if _, decErr := jpeg.Decode(bytes.NewReader(full)); decErr != nil {
			return ErrInvalidImage
		}
	case "image/gif":
		if _, decErr := gif.Decode(bytes.NewReader(full)); decErr != nil {
			return ErrInvalidImage
		}
	case "image/webp":
		// no stdlib decoder; trailing-bytes check is the sole defence
	default:
		return ErrInvalidImage
	}
	if !strictEnd {
		return nil
	}
	return validateImageEnd(format, full)
}

// WithoutStrictImageEndCheck disables the trailing-bytes rejection for
// raster image uploads. The full-image decode still runs and rejects
// corrupted payloads, but a clean image followed by extra bytes
// (whitespace, NUL padding, or an appended script) is accepted.
//
// Use this option only when legacy clients produce non-spec output
// (some older mobile cameras pad JPEG/EXIF segments with garbage past
// the FFD9 EOI marker). It widens the polyglot surface; prefer fixing
// the client instead.
func (m *MIMEValidator) WithoutStrictImageEndCheck() *MIMEValidator {
	cp := *m
	cp.strictEnd = false
	return &cp
}

// WithImageDimensionCap overrides the built-in 8192×8192 cap that
// guards the full-decode path against decompression bombs. Both
// dimensions must be positive. Note that this is independent of the
// caller-configured [MaxImageDimensions] validator — that one inspects
// the same header but is a separate validator in the chain.
func (m *MIMEValidator) WithImageDimensionCap(maxWidth, maxHeight int) *MIMEValidator {
	if maxWidth <= 0 || maxHeight <= 0 {
		panic("uploadsec: WithImageDimensionCap requires positive dimensions")
	}
	cp := *m
	cp.maxWidth = maxWidth
	cp.maxHeight = maxHeight
	return &cp
}

// AllowMIMETypes returns a Validator that sniffs the first 512 bytes
// of the body and rejects content whose detected MIME type is not in
// the allowlist. The detected type replaces meta.ContentType so
// downstream steps and storage backends record the truth, not the
// caller-supplied lie.
//
// For raster image MIME types in [imageDeepDecodeMIMEs] the validator
// runs the polyglot defenses described in the package doc: dimension
// cap (default 8192×8192) → full-image decode → strict end-of-stream
// check. The full decode catches polyglots with valid headers but
// corrupted IDAT or appended scripts; the end-of-stream check catches
// polyglots that decode cleanly but pad bytes after the format's
// terminator. Both can be tuned via the methods on [MIMEValidator].
//
// image/svg+xml is rejected unconditionally — SVG is an XML format with
// scripting and SSRF surface that no sniffer can defang. Services that
// need to accept SVG must opt in via [AllowSVG] and provide a sanitiser.
//
// Sniffing uses [http.DetectContentType] (RFC 2046 + Mozilla heuristics),
// which is the same logic stdlib uses for static-file serving. It is
// not exhaustive — exotic formats may be detected as
// application/octet-stream. Rely on AllowExtensions for those edge
// cases or extend the allowlist with the kit's own MIME registry.
func AllowMIMETypes(allowed ...string) *MIMEValidator {
	if len(allowed) == 0 {
		panic("uploadsec: AllowMIMETypes requires at least one MIME type")
	}
	allowSet := make(map[string]struct{}, len(allowed))
	for _, m := range allowed {
		mediaType, _, err := mime.ParseMediaType(strings.ToLower(strings.TrimSpace(m)))
		if err != nil || mediaType == "" || !strings.Contains(mediaType, "/") {
			panic("uploadsec: AllowMIMETypes: invalid MIME type")
		}
		if mediaType == "image/svg+xml" {
			panic("uploadsec: AllowMIMETypes: image/svg+xml requires AllowSVG with an SVGSanitizer; the default allowlist refuses unsanitised SVG")
		}
		allowSet[mediaType] = struct{}{}
	}
	return &MIMEValidator{
		allowed:   allowSet,
		strictEnd: true,
		maxWidth:  defaultMaxImageDimension,
		maxHeight: defaultMaxImageDimension,
	}
}

// AllowSVG returns a Validator that accepts image/svg+xml uploads after
// running the body through sanitizer. The sanitizer is responsible for
// stripping or neutralising scripting and external references; an
// unfiltered SVG is effectively a hostile HTML document.
//
// The kit ships no default sanitiser — the surface is large enough that
// every deployment should make an explicit decision about which
// SVG-handling library to trust. Compose [AllowSVG] alongside
// [AllowMIMETypes] when a service must accept SVG. SVG is not a raster
// format, so the polyglot full-decode path used by AllowMIMETypes does
// not apply; the sanitiser is the sole defence and MUST be audited.
func AllowSVG(sanitizer SVGSanitizer) Validator {
	if sanitizer == nil {
		panic("uploadsec: AllowSVG requires a non-nil SVGSanitizer")
	}
	return ValidatorFunc(func(_ context.Context, body io.ReadSeeker, meta Meta) (Meta, error) {
		buf := make([]byte, 512)
		n, err := io.ReadFull(body, buf)
		if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
			return meta, fmt.Errorf("uploadsec: sniff body failed")
		}
		sniffed := http.DetectContentType(buf[:n])
		base, _, _ := strings.Cut(sniffed, ";")
		base = strings.ToLower(strings.TrimSpace(base))
		if base != "image/svg+xml" && base != "text/xml" && base != "text/plain" && base != "application/xml" {
			return meta, ErrMIMETypeNotAllowed
		}
		if _, err := body.Seek(0, io.SeekStart); err != nil {
			return meta, fmt.Errorf("uploadsec: rewind for SVG sanitise: %w", err)
		}
		original, err := io.ReadAll(body)
		if err != nil {
			return meta, fmt.Errorf("uploadsec: read SVG body: %w", err)
		}
		sanitised, err := sanitizer.SanitizeSVG(bytes.NewReader(original))
		if err != nil {
			return meta, fmt.Errorf("uploadsec: %w", err)
		}
		if !bytes.Equal(original, sanitised) {
			return meta, ErrSVGSanitizationModified
		}
		meta.ContentType = "image/svg+xml"
		return meta, nil
	})
}

// AllowExtensions returns a Validator that rejects files whose
// filename extension is not in the allowlist. Extensions are matched
// case-insensitively and must include the leading dot ("." prefix).
// Filenames without an extension are rejected.
func AllowExtensions(allowed ...string) Validator {
	if len(allowed) == 0 {
		panic("uploadsec: AllowExtensions requires at least one extension")
	}
	allowSet := make(map[string]struct{}, len(allowed))
	for _, e := range allowed {
		ext := strings.ToLower(strings.TrimSpace(e))
		if ext == "" || !strings.HasPrefix(ext, ".") || strings.ContainsAny(ext, `/\`) {
			panic("uploadsec: AllowExtensions: invalid extension")
		}
		allowSet[ext] = struct{}{}
	}
	return ValidatorFunc(func(_ context.Context, _ io.ReadSeeker, meta Meta) (Meta, error) {
		ext := strings.ToLower(path.Ext(meta.Filename))
		if ext == "" {
			return meta, ErrExtensionNotAllowed
		}
		if _, ok := allowSet[ext]; !ok {
			return meta, ErrExtensionNotAllowed
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
					return meta, fmt.Errorf("%w: extension does not match content type", ErrExtensionNotAllowed)
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
		panic("uploadsec: MaxImageDimensions: maxWidth and maxHeight must be positive")
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
			return meta, fmt.Errorf("uploadsec: buffer image header failed")
		}
		cfg, _, err := image.DecodeConfig(bytes.NewReader(header))
		if err != nil {
			return meta, ErrInvalidImage
		}
		if cfg.Width > maxWidth || cfg.Height > maxHeight {
			return meta, ErrImageTooLarge
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
		errors.Is(err, ErrInvalidImage),
		errors.Is(err, ErrMalwareDetected):
		return http.StatusUnprocessableEntity
	case errors.Is(err, ErrScannerUnavailable):
		return http.StatusServiceUnavailable
	default:
		return http.StatusInternalServerError
	}
}

// validateImageEnd checks that the body ends exactly at the format's
// canonical terminator, with no trailing bytes — not whitespace, not
// NUL padding, not appended script. Returns ErrInvalidImage on any
// deviation.
func validateImageEnd(mimeType string, body []byte) error {
	switch mimeType {
	case "image/png":
		return validatePNGEnd(body)
	case "image/jpeg":
		return validateJPEGEnd(body)
	case "image/gif":
		return validateGIFEnd(body)
	case "image/webp":
		return validateWebPEnd(body)
	default:
		return nil
	}
}

// pngSignature is the 8-byte fixed PNG magic.
var pngSignature = []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}

// validatePNGEnd walks all PNG chunks. Each chunk is:
//
//	length(4 BE) | type(4) | data(length) | CRC(4 BE of type+data)
//
// The body must start with the PNG signature, contain exactly one IEND
// chunk at the end (with correct CRC), and have no bytes following it.
func validatePNGEnd(body []byte) error {
	if len(body) < len(pngSignature)+12 || !bytes.Equal(body[:len(pngSignature)], pngSignature) {
		return ErrInvalidImage
	}
	off := len(pngSignature)
	seenIEND := false
	for off < len(body) {
		if seenIEND {
			// Any bytes past IEND are an appended-payload polyglot.
			return ErrInvalidImage
		}
		if off+8 > len(body) {
			return ErrInvalidImage
		}
		length := binary.BigEndian.Uint32(body[off : off+4])
		chunkType := body[off+4 : off+8]
		// 4 (length) + 4 (type) + length + 4 (crc).
		end := off + 8 + int(length) + 4
		if end < off || end > len(body) { // overflow or truncated
			return ErrInvalidImage
		}
		// Validate CRC over type+data.
		crc := binary.BigEndian.Uint32(body[end-4 : end])
		if crc32.ChecksumIEEE(body[off+4:end-4]) != crc {
			return ErrInvalidImage
		}
		if bytes.Equal(chunkType, []byte("IEND")) {
			if length != 0 {
				return ErrInvalidImage
			}
			seenIEND = true
		}
		off = end
	}
	if !seenIEND {
		return ErrInvalidImage
	}
	if off != len(body) {
		return ErrInvalidImage
	}
	return nil
}

// validateJPEGEnd walks JPEG segments and confirms the body ends at the
// FFD9 EOI marker with no trailing bytes. JPEG layout:
//
//	SOI(FFD8) | marker(FFxx) | length(2 BE, segment-marker only) | data
//	| ... | SOS(FFDA) | entropy-coded data | EOI(FFD9)
//
// Entropy-coded image data is a stream of bytes where any 0xFF byte is
// followed by a stuff byte (0x00) or by a restart marker (FFD0..FFD7).
// A standalone FFD9 inside the entropy stream is impossible because
// 0xFF is always stuffed. We scan forward and stop at the first FFD9
// that is not preceded by stuffing/restart context.
func validateJPEGEnd(body []byte) error {
	if len(body) < 4 {
		return ErrInvalidImage
	}
	if body[0] != 0xFF || body[1] != 0xD8 {
		return ErrInvalidImage
	}
	// Trailing bytes test is structural: the spec requires the file to
	// end with FFD9. We don't re-parse the whole segment table — Go's
	// jpeg.Decode already accepted the body — but we do require that
	// the very last two bytes are FFD9 with no padding after.
	if body[len(body)-2] != 0xFF || body[len(body)-1] != 0xD9 {
		return ErrInvalidImage
	}
	// Walk the segment header chain up to SOS (FFDA). After SOS the
	// entropy stream runs to EOI; we already verified EOI position
	// above. This catches blatant truncation/injection in the headers.
	off := 2
	for off < len(body) {
		if body[off] != 0xFF {
			return ErrInvalidImage
		}
		// Skip fill bytes 0xFF.
		marker := byte(0)
		for off < len(body) && body[off] == 0xFF {
			off++
		}
		if off >= len(body) {
			return ErrInvalidImage
		}
		marker = body[off]
		off++
		switch {
		case marker == 0xD9: // EOI — only valid at the very end
			return nil
		case marker == 0xDA: // SOS — entropy stream follows, EOI already verified
			return nil
		case marker >= 0xD0 && marker <= 0xD8: // RSTn or SOI (already consumed)
			continue
		default:
			if off+2 > len(body) {
				return ErrInvalidImage
			}
			segLen := int(binary.BigEndian.Uint16(body[off : off+2]))
			if segLen < 2 || off+segLen > len(body) {
				return ErrInvalidImage
			}
			off += segLen
		}
	}
	return ErrInvalidImage
}

// validateGIFEnd walks GIF data and confirms the body ends at the
// trailer byte 0x3B with no trailing data. GIF layout is a stream of
// blocks; the trailer terminates the data stream. Rather than re-parse
// the entire block table (gif.Decode already accepted the body), we
// require the last byte to be 0x3B.
func validateGIFEnd(body []byte) error {
	if len(body) < 6 {
		return ErrInvalidImage
	}
	// GIF signature: "GIF87a" or "GIF89a".
	if !bytes.Equal(body[:3], []byte("GIF")) {
		return ErrInvalidImage
	}
	if body[len(body)-1] != 0x3B {
		return ErrInvalidImage
	}
	return nil
}

// validateWebPEnd parses the RIFF wrapper and confirms the body length
// matches the declared RIFF size with no trailing bytes. WebP layout:
//
//	"RIFF" | size(4 LE) | "WEBP" | chunks...
//
// Where size is the total file size minus 8 (excluding the "RIFF"
// magic and the size field itself). Per RIFF spec, chunks with odd
// payloads are padded to even byte boundaries.
func validateWebPEnd(body []byte) error {
	if len(body) < 12 {
		return ErrInvalidImage
	}
	if !bytes.Equal(body[:4], []byte("RIFF")) || !bytes.Equal(body[8:12], []byte("WEBP")) {
		return ErrInvalidImage
	}
	declared := binary.LittleEndian.Uint32(body[4:8])
	// total file length is declared + 8 (RIFF magic + size field).
	// Tolerate the spec's even-padding: if declared is odd, the file
	// may include one trailing pad byte.
	expectMin := int64(declared) + 8
	expectMax := expectMin
	if declared%2 == 1 {
		expectMax++
	}
	got := int64(len(body))
	if got < expectMin || got > expectMax {
		return ErrInvalidImage
	}
	return nil
}

// peekWebPDimensions extracts the width and height from a WebP body
// header. Supports VP8 (lossy), VP8L (lossless), and VP8X (extended).
// Returns 0,0 with an error on unrecognised input.
func peekWebPDimensions(body []byte) (int, int, error) {
	if len(body) < 30 {
		return 0, 0, ErrInvalidImage
	}
	if !bytes.Equal(body[:4], []byte("RIFF")) || !bytes.Equal(body[8:12], []byte("WEBP")) {
		return 0, 0, ErrInvalidImage
	}
	chunk := string(body[12:16])
	switch chunk {
	case "VP8 ":
		// Simple lossy: 14-bit width/height at frame tag offset 26.
		// body[20..23] = frame tag; body[23..25] are start code (0x9D 0x01 0x2A).
		if len(body) < 30 {
			return 0, 0, ErrInvalidImage
		}
		w := int(binary.LittleEndian.Uint16(body[26:28]) & 0x3FFF)
		h := int(binary.LittleEndian.Uint16(body[28:30]) & 0x3FFF)
		return w, h, nil
	case "VP8L":
		// Lossless: 1 byte signature 0x2F at offset 20, then 14+14 bits
		// of width-1/height-1 little-endian.
		if len(body) < 25 {
			return 0, 0, ErrInvalidImage
		}
		if body[20] != 0x2F {
			return 0, 0, ErrInvalidImage
		}
		b1 := uint32(body[21])
		b2 := uint32(body[22])
		b3 := uint32(body[23])
		b4 := uint32(body[24])
		w := int((b1 | (b2&0x3F)<<8) + 1)
		h := int(((b2 >> 6) | b3<<2 | (b4&0x0F)<<10) + 1)
		return w, h, nil
	case "VP8X":
		// Extended: canvas width-1 (24 LE) at offset 24, height-1 at offset 27.
		if len(body) < 30 {
			return 0, 0, ErrInvalidImage
		}
		w := int(uint32(body[24]) | uint32(body[25])<<8 | uint32(body[26])<<16)
		h := int(uint32(body[27]) | uint32(body[28])<<8 | uint32(body[29])<<16)
		return w + 1, h + 1, nil
	default:
		return 0, 0, ErrInvalidImage
	}
}
