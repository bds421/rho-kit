package storagehttp

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"path"
	"strings"
	"unicode/utf8"

	"github.com/bds421/rho-kit/core/v2/redact"
	"github.com/bds421/rho-kit/infra/v2/storage"
)

// ServeOptions configures [ServeFile] behavior.
type ServeOptions struct {
	// ContentDisposition controls the Content-Disposition header.
	// "attachment" (default) forces download and avoids stored-XSS when
	// user-influenced Content-Type is served. "inline" displays in-browser
	// and should only be used for types the operator trusts to render.
	ContentDisposition string

	// Filename overrides the filename in the Content-Disposition header.
	// If empty, the last segment of the storage key is used.
	Filename string

	// CacheControl sets the Cache-Control header value.
	// If empty, no Cache-Control header is set.
	CacheControl string
}

// ServeFile streams a stored object as an HTTP response.
//
// It sets Content-Type, Content-Length, Content-Disposition, and ETag headers.
// When the backend returns an [io.ReadSeeker] (e.g., local filesystem),
// [http.ServeContent] is used to enable Range request support.
// Otherwise, the content is streamed directly with [io.Copy].
//
// Conditional-GET: if the backend provides an ETag (via ObjectMeta.ETag),
// it is sent as an ETag header. If the client sends If-None-Match matching
// the ETag, a 304 Not Modified is returned without streaming the body.
//
// Returns an error wrapping [storage.ErrObjectNotFound] when the key
// does not exist — callers should map this to HTTP 404.
//
// Streaming-error caveat: in the non-seekable streaming fallback path, Go
// flushes a 200 status on the first body Write. If io.Copy then fails
// partway through, ServeFile still returns that error, but the client has
// already received a 200 with a TRUNCATED, corrupt body — the status can no
// longer be changed. Callers MUST NOT attempt to map this returned error to
// an HTTP 5xx (e.g. http.Error / WriteHeader(500)): doing so is a no-op or
// panics on a second WriteHeader. Instead, log and alert so the truncated
// download is observable, and treat the error as a delivery failure rather
// than a request-handling failure. (The seekable path via http.ServeContent
// is not affected: it can still emit a Range/precondition error response.)
func ServeFile(w http.ResponseWriter, r *http.Request, backend storage.Storage, key string, opts ServeOptions) error {
	if w == nil {
		return fmt.Errorf("storagehttp: response writer is required")
	}
	if r == nil {
		return fmt.Errorf("storagehttp: request is required")
	}
	if backend == nil {
		return fmt.Errorf("storagehttp: backend is required")
	}
	if err := storage.ValidateKey(key); err != nil {
		return err
	}
	serveCfg, err := normalizeServeOptions(key, opts)
	if err != nil {
		return err
	}

	// Prefer Stat/Head for conditional-GET so a 304 does not open the body.
	rc, meta, err := statOrOpen(r.Context(), backend, key)
	if err != nil {
		return err
	}
	defer func() {
		if rc != nil {
			_ = rc.Close()
		}
	}()

	contentType := responseContentType(meta.ContentType, serveCfg.filename, key)

	// Restrict Content-Disposition to a safe allowlist to prevent header injection.
	w.Header().Set("Content-Disposition", mime.FormatMediaType(serveCfg.disposition, map[string]string{
		"filename": serveCfg.filename,
	}))

	if serveCfg.cacheControl != "" {
		w.Header().Set("Cache-Control", serveCfg.cacheControl)
	}

	if notModified := setETag(w, r, key, meta.ETag); notModified {
		w.WriteHeader(http.StatusNotModified)
		return nil
	}

	// Body needed: open via Get when we only Stat'ed above.
	if rc == nil {
		rc, meta, err = backend.Get(r.Context(), key)
		if err != nil {
			return err
		}
	}

	// User-uploaded content is served back with the metadata-recorded
	// Content-Type. Force browsers to honor that type instead of sniffing —
	// a polyglot upload (PNG header + JS payload) served as image/png
	// would otherwise be sniffed-and-executed by older browsers.
	w.Header().Set("X-Content-Type-Options", "nosniff")

	// If the reader supports seeking, use http.ServeContent for Range support.
	// http.ServeContent handles If-Modified-Since and Range internally.
	if rs, ok := rc.(io.ReadSeeker); ok {
		w.Header().Set("Content-Type", contentType)
		http.ServeContent(w, r, serveCfg.filename, meta.LastModified, rs)
		return nil
	}

	// Streaming fallback — no Range support.
	// Do not call WriteHeader explicitly; Go sends 200 on the first Write.
	// This allows middleware to still modify headers or set status codes.
	//
	// Content-Length is intentionally omitted for non-seekable streams.
	// If the backend reports an incorrect Size, setting Content-Length
	// causes clients to hang waiting for the declared number of bytes.
	// Without Content-Length, Go uses chunked transfer encoding, which
	// gracefully handles size mismatches.
	w.Header().Set("Content-Type", contentType)
	_, err = io.Copy(w, rc)
	return err
}

type serveConfig struct {
	disposition  string
	filename     string
	cacheControl string
}

func normalizeServeOptions(key string, opts ServeOptions) (serveConfig, error) {
	disposition := opts.ContentDisposition
	// Default to attachment: user-influenced Content-Type served
	// Content-Disposition: inline is a stored-XSS vector for
	// text/html, image/svg+xml, text/xml, etc. Callers that need
	// browser rendering (PDFs, images they trust) must opt in with
	// ContentDisposition: "inline".
	if disposition != "inline" && disposition != "attachment" {
		disposition = "attachment"
	}
	filename := safeDownloadFilename(opts.Filename, path.Base(key))
	cacheControl := strings.TrimSpace(opts.CacheControl)
	if cacheControl != "" && !safeHeaderValue(cacheControl) {
		return serveConfig{}, fmt.Errorf("storagehttp: Cache-Control contains invalid characters")
	}
	return serveConfig{
		disposition:  disposition,
		filename:     filename,
		cacheControl: cacheControl,
	}, nil
}

func safeDownloadFilename(name, fallback string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		name = fallback
	}
	name = strings.ReplaceAll(name, "\\", "/")
	name = path.Base(name)

	var b strings.Builder
	for _, r := range name {
		if r < 0x20 || r == 0x7f || r == '/' || r == '\\' {
			continue
		}
		b.WriteRune(r)
		if b.Len() >= 128 {
			break
		}
	}
	cleaned := strings.TrimSpace(b.String())
	if cleaned == "" || cleaned == "." || cleaned == ".." {
		return "download"
	}
	return cleaned
}

func safeHeaderValue(value string) bool {
	if len(value) > 512 {
		return false
	}
	if !utf8.ValidString(value) {
		return false
	}
	for i := 0; i < len(value); i++ {
		if value[i] < 0x20 || value[i] > 0x7e {
			return false
		}
	}
	return true
}

// statOrOpen reads the object's metadata through Stat when the backend has
// it, leaving the body unopened (nil reader); otherwise it opens the object.
func statOrOpen(ctx context.Context, backend storage.Storage, key string) (io.ReadCloser, storage.ObjectMeta, error) {
	if st, ok := storage.AsStatter(backend); ok {
		meta, err := st.Stat(ctx, key)
		return nil, meta, err
	}
	return backend.Get(ctx, key)
}

// setETag sends the backend's ETag for conditional-GET support and reports
// whether the request's If-None-Match matches it. An ETag the header cannot
// carry safely is skipped.
func setETag(w http.ResponseWriter, r *http.Request, key, backendETag string) bool {
	if backendETag == "" {
		return false
	}
	etag, ok := quotedETag(backendETag)
	if !ok {
		slog.Warn("storagehttp: ETag contains invalid characters, skipping",
			redact.String("key", key))
		return false
	}
	w.Header().Set("ETag", etag)
	inm := strings.Join(r.Header.Values("If-None-Match"), ",")
	return inm != "" && etagMatch(inm, etag)
}

// quotedETag returns etag in its quoted form, keeping a weak W/ prefix
// (RFC 7232). It reports false for control characters (CR, LF, NUL) or
// internal double quotes.
func quotedETag(etag string) (string, bool) {
	weak := false
	opaque := etag
	if len(opaque) >= 2 && (opaque[0] == 'W' || opaque[0] == 'w') && opaque[1] == '/' {
		weak = true
		opaque = opaque[2:]
	}
	quoted := len(opaque) >= 2 && opaque[0] == '"' && opaque[len(opaque)-1] == '"'
	inner := opaque
	if quoted {
		inner = opaque[1 : len(opaque)-1]
	}
	if strings.ContainsAny(inner, "\r\n\x00\"") {
		return "", false
	}
	if !quoted {
		opaque = `"` + opaque + `"`
	}
	if weak {
		opaque = "W/" + opaque
	}
	return opaque, true
}

// etagMatch checks if serverETag matches any ETag in the If-None-Match header value.
// Handles the W/ (weak) prefix per RFC 7232 §2.3: weak comparison ignores the prefix.
// The inm value may contain multiple comma-separated ETags (e.g., `"a", W/"b"`).
func etagMatch(inm, serverETag string) bool {
	if inm == "*" {
		return true
	}
	// Strip W/ prefix for weak comparison (RFC 7232 §2.3.2).
	serverStrong := stripWeakPrefix(serverETag)

	for {
		inm = strings.TrimLeft(inm, " ,")
		if inm == "" {
			return false
		}
		candidate := stripWeakPrefix(inm)
		end := closingQuote(candidate)
		if end < 0 {
			// Malformed tokens are skipped so a later well-formed match
			// still yields 304 (RFC 7232 §3.2 allows lists).
			inm = nextListElement(inm)
			continue
		}
		if candidate[:end+1] == serverStrong {
			return true
		}
		inm = inm[len(inm)-len(candidate)+end+1:]
	}
}

// closingQuote returns the index of the quote that closes the entity tag
// opening candidate, or -1 when candidate does not start with a complete
// quoted tag.
func closingQuote(candidate string) int {
	if len(candidate) < 2 || candidate[0] != '"' {
		return -1
	}
	end := strings.IndexByte(candidate[1:], '"')
	if end < 0 {
		return -1
	}
	return end + 1
}

// nextListElement drops the list element inm starts with, up to the next
// comma.
func nextListElement(inm string) string {
	if i := strings.IndexByte(inm, ','); i >= 0 {
		return inm[i:]
	}
	return ""
}

func stripWeakPrefix(s string) string {
	if len(s) >= 2 && s[0] == 'W' && s[1] == '/' {
		return s[2:]
	}
	return s
}

func responseContentType(metaContentType, filename, key string) string {
	contentType := strings.TrimSpace(metaContentType)
	if contentType != "" {
		if err := storage.ValidateObjectMeta(storage.ObjectMeta{ContentType: contentType}); err == nil {
			return contentType
		}
		slog.Warn("storagehttp: Content-Type metadata is invalid, falling back",
			redact.String("key", key))
	}
	if byExt := mime.TypeByExtension(path.Ext(filename)); byExt != "" {
		return byExt
	}
	return "application/octet-stream"
}
