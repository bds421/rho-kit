package storage

import (
	"bytes"
	"context"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"mime"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/bds421/rho-kit/core/v2/config"
	"github.com/gabriel-vasile/mimetype"
)

// MaxInstanceNameBytes caps storage backend instance names used in metrics and
// traces. Instance names should be short, static labels such as "avatars" or
// "documents", not request or tenant identifiers.
const MaxInstanceNameBytes = 256

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
// A returned reader that also implements io.Closer transfers cleanup ownership
// to the validation chain and storage backend.
type Validator func(ctx context.Context, r io.Reader, meta *ObjectMeta) (io.Reader, error)

// ValidateEndpointURL checks an optional storage service endpoint override
// and rejects plain http. Empty endpoints are valid and mean "use the
// provider default". Non-empty endpoints must be absolute https URLs with
// a host and no embedded credentials, query, or fragment.
//
// Use [ValidateEndpointURLAllowingInsecure] for development setups that
// genuinely need http (loopback test fixtures); the split is intentional
// so the insecure path is never reachable behind a flipped boolean at the
// call site.
func ValidateEndpointURL(name, rawURL string) error {
	return validateEndpointURL(name, rawURL, false)
}

// ValidateEndpointURLAllowingInsecure is [ValidateEndpointURL] with
// plain http permitted. Reserved for explicit dev-only overrides where
// the operator has acknowledged the loss of transport security.
func ValidateEndpointURLAllowingInsecure(name, rawURL string) error {
	return validateEndpointURL(name, rawURL, true)
}

func validateEndpointURL(name, rawURL string, allowInsecure bool) error {
	if rawURL == "" {
		return nil
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("%s is invalid", name)
	}
	if !u.IsAbs() {
		return fmt.Errorf("%s must be an absolute URL with a host", name)
	}
	if err := config.ValidateURLHost(name, u); err != nil {
		return err
	}
	if u.User != nil {
		return fmt.Errorf("%s must not contain credentials", name)
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("%s must not contain query or fragment components", name)
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		if allowInsecure {
			return nil
		}
		return fmt.Errorf("%s must use https unless the insecure endpoint opt-in is enabled", name)
	default:
		return fmt.Errorf("%s scheme must be https, or http with the insecure endpoint opt-in", name)
	}
}

// RedactedEndpointURL renders an endpoint override for logs. It keeps the
// scheme, host, and path useful for diagnosis while dropping components that
// commonly carry credentials in invalid or legacy configs.
func RedactedEndpointURL(rawURL string) string {
	if rawURL == "" {
		return ""
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return "[INVALID URL]"
	}
	redacted := *u
	redacted.User = nil
	redacted.RawQuery = ""
	redacted.Fragment = ""
	return redacted.String()
}

// ValidateInstanceName checks that a storage backend instance label is safe
// for metrics, traces, and logs.
func ValidateInstanceName(name string) error {
	if name == "" {
		return fmt.Errorf("storage: instance name must not be empty")
	}
	if len(name) > MaxInstanceNameBytes {
		return fmt.Errorf("storage: instance name exceeds maximum length")
	}
	if containsInvalidInstanceNameRune(name) {
		return fmt.Errorf("storage: instance name contains invalid characters")
	}
	return nil
}

func containsInvalidInstanceNameRune(name string) bool {
	if !utf8.ValidString(name) {
		return true
	}
	for _, r := range name {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return true
		}
	}
	return false
}

// CloneValidators returns a detached copy of validators.
//
// It panics on nil validators so backend options fail at construction time
// instead of storing a latent nil function that panics on the first upload.
func CloneValidators(validators ...Validator) []Validator {
	if len(validators) == 0 {
		return nil
	}
	out := make([]Validator, len(validators))
	for i, v := range validators {
		if v == nil {
			panic("storage: validator must not be nil")
		}
		out[i] = v
	}
	return out
}

// AppendValidators appends a validated, detached copy of validators to dst.
func AppendValidators(dst []Validator, validators ...Validator) []Validator {
	return append(dst, CloneValidators(validators...)...)
}

// AllowedMIMETypes returns a Validator that detects the actual MIME type
// by sniffing the first bytes of content (not the file extension or
// declared Content-Type). The sniffed bytes are prepended back to the
// reader so the full content is available to the backend.
//
// The detected type overwrites meta.ContentType. If the detected type
// is not in the allowed set, returns an error wrapping ErrValidation.
func AllowedMIMETypes(allowed ...string) Validator {
	if len(allowed) == 0 {
		panic("storage: AllowedMIMETypes requires at least one MIME type")
	}
	exact := make(map[string]struct{}, len(allowed))
	var wildcards []string // e.g. "image/*" → prefix "image/"
	for _, t := range allowed {
		mediaType, wildcard := normalizeAllowedMIMEType(t)
		if wildcard {
			wildcards = append(wildcards, strings.TrimSuffix(mediaType, "*"))
		} else {
			exact[mediaType] = struct{}{}
		}
	}

	return func(_ context.Context, r io.Reader, meta *ObjectMeta) (io.Reader, error) {
		header := make([]byte, mimeSniffSize)
		n, err := io.ReadFull(r, header)
		if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
			return nil, fmt.Errorf("storage: read for MIME detection failed")
		}
		header = header[:n]

		detected := normalizeDetectedMIMEType(mimetype.Detect(header).String())
		meta.ContentType = detected

		if _, ok := exact[detected]; ok {
			return prependReader(header, r), nil
		}
		for _, prefix := range wildcards {
			if strings.HasPrefix(detected, prefix) {
				return prependReader(header, r), nil
			}
		}

		return nil, fmt.Errorf("%w: MIME type is not allowed", ErrValidation)
	}
}

func normalizeAllowedMIMEType(value string) (mediaType string, wildcard bool) {
	trimmed := strings.ToLower(strings.TrimSpace(value))
	if trimmed == "" {
		panic("storage: AllowedMIMETypes requires non-empty MIME types")
	}
	if strings.HasSuffix(trimmed, "/*") {
		prefix := strings.TrimSuffix(trimmed, "/*")
		if prefix == "" || strings.ContainsAny(prefix, ";/ \t\r\n") {
			panic("storage: invalid MIME wildcard")
		}
		return prefix + "/*", true
	}
	mediaType, _, err := mime.ParseMediaType(trimmed)
	if err != nil || mediaType == "" || !strings.Contains(mediaType, "/") {
		panic("storage: invalid MIME type")
	}
	return mediaType, false
}

func normalizeDetectedMIMEType(value string) string {
	mediaType, _, err := mime.ParseMediaType(strings.ToLower(strings.TrimSpace(value)))
	if err != nil || mediaType == "" {
		return strings.ToLower(strings.TrimSpace(value))
	}
	return mediaType
}

// MaxFileSize returns a Validator that enforces a maximum content size.
// If meta.Size is set and already exceeds maxBytes, the error is returned
// immediately without reading any content. Otherwise, a limit-enforcing
// reader wraps the stream so the limit is checked during consumption.
//
// FR-080 [MED]: panics on maxBytes <= 0 — a zero or negative limit
// would either reject every upload or behave unpredictably under
// direct use of [limitReader].
func MaxFileSize(maxBytes int64) Validator {
	if maxBytes <= 0 {
		panic("storage: MaxFileSize requires maxBytes > 0")
	}
	return func(_ context.Context, r io.Reader, meta *ObjectMeta) (io.Reader, error) {
		if meta.Size > 0 && meta.Size > maxBytes {
			return nil, fmt.Errorf("%w: declared size exceeds maximum", ErrValidation)
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
		return 0, fmt.Errorf("%w: content exceeds maximum size", ErrValidation)
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
		return valid, fmt.Errorf("%w: content exceeds maximum size", ErrValidation)
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
	if minW < 0 || minH < 0 || maxW < 0 || maxH < 0 {
		panic("storage: ImageDimensions requires non-negative bounds")
	}
	if maxW > 0 && minW > maxW {
		panic("storage: ImageDimensions requires minW <= maxW when maxW is set")
	}
	if maxH > 0 && minH > maxH {
		panic("storage: ImageDimensions requires minH <= maxH when maxH is set")
	}

	return func(_ context.Context, r io.Reader, meta *ObjectMeta) (io.Reader, error) {
		var buf bytes.Buffer
		lr := io.LimitReader(r, imageDimensionReadLimit)
		tr := io.TeeReader(lr, &buf)

		cfg, _, err := image.DecodeConfig(tr)
		if err != nil {
			return nil, fmt.Errorf("%w: cannot decode image dimensions", ErrValidation)
		}

		if cfg.Width < minW || cfg.Height < minH {
			return nil, fmt.Errorf("%w: image is smaller than minimum dimensions", ErrValidation)
		}
		if maxW > 0 && cfg.Width > maxW {
			return nil, fmt.Errorf("%w: image width exceeds maximum", ErrValidation)
		}
		if maxH > 0 && cfg.Height > maxH {
			return nil, fmt.Errorf("%w: image height exceeds maximum", ErrValidation)
		}

		return prependReader(buf.Bytes(), r), nil
	}
}

// ApplyValidators runs validators in sequence on the given reader.
// If any validator returns an error, the chain stops immediately.
// The returned reader is the validated, potentially-wrapped stream
// ready for the backend.
func ApplyValidators(ctx context.Context, r io.Reader, meta *ObjectMeta, validators []Validator) (io.Reader, error) {
	if ctx == nil {
		return nil, fmt.Errorf("storage: context must not be nil")
	}
	if r == nil {
		return nil, fmt.Errorf("%w: reader must not be nil", ErrValidation)
	}
	if meta == nil {
		return nil, fmt.Errorf("%w: object metadata must not be nil", ErrValidation)
	}
	for _, v := range validators {
		if v == nil {
			closeReader(r)
			return nil, fmt.Errorf("%w: validator must not be nil", ErrValidation)
		}
		current := r
		next, err := v(ctx, current, meta)
		if err != nil {
			closeReader(current)
			return nil, err
		}
		if next == nil {
			closeReader(current)
			return nil, fmt.Errorf("%w: validator returned nil reader", ErrValidation)
		}
		r = next
	}
	return r, nil
}

type closeAwareReader struct {
	io.Reader
	closer io.Closer
}

func (r *closeAwareReader) Close() error {
	return r.closer.Close()
}

func prependReader(prefix []byte, r io.Reader) io.Reader {
	reader := io.MultiReader(bytes.NewReader(prefix), r)
	if closer, ok := r.(io.Closer); ok {
		return &closeAwareReader{Reader: reader, closer: closer}
	}
	return reader
}

func closeReader(r io.Reader) {
	_ = CloseValidatedReader(r)
}

// CloseValidatedReader closes r when it implements [io.Closer].
//
// Backends and HTTP adapters should defer this after a successful
// [ApplyValidators] call when at least one validator ran. Validators may return
// cleanup-owning readers (for example temp-file replays or scanner spool
// files); closing the returned reader guarantees those resources are released
// even when later metadata validation fails or a provider SDK aborts before
// reading to EOF. Do not call this for a caller-owned Put reader when no
// validators ran.
func CloseValidatedReader(r io.Reader) error {
	if closer, ok := r.(io.Closer); ok {
		return closer.Close()
	}
	return nil
}
