package storagehttp

import (
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
	var (
		rc   io.ReadCloser
		meta storage.ObjectMeta
	)
	if st, ok := storage.AsStatter(backend); ok {
		meta, err = st.Stat(r.Context(), key)
		if err != nil {
			return err
		}
	} else {
		rc, meta, err = backend.Get(r.Context(), key)
		if err != nil {
			return err
		}
		defer func() {
			if rc != nil {
				_ = rc.Close()
			}
		}()
	}

	contentType := responseContentType(meta.ContentType, serveCfg.filename, key)

	// Restrict Content-Disposition to a safe allowlist to prevent header injection.
	w.Header().Set("Content-Disposition", mime.FormatMediaType(serveCfg.disposition, map[string]string{
		"filename": serveCfg.filename,
	}))

	if serveCfg.cacheControl != "" {
		w.Header().Set("Cache-Control", serveCfg.cacheControl)
	}

	// Set ETag for conditional-GET support.
	if meta.ETag != "" {
		etag := meta.ETag
		// Reject ETags containing control characters (CR, LF, NUL) or internal
		// double quotes. Weak ETags (W/"...") are accepted per RFC 7232.
		stripped := etag
		if len(stripped) >= 2 && (stripped[0] == 'W' || stripped[0] == 'w') && stripped[1] == '/' {
			stripped = stripped[2:]
		}
		if len(stripped) >= 2 && stripped[0] == '"' && stripped[len(stripped)-1] == '"' {
			stripped = stripped[1 : len(stripped)-1]
		}
		if strings.ContainsAny(stripped, "\r\n\x00\"") {
			slog.Warn("storagehttp: ETag contains invalid characters, skipping",
				redact.String("key", key))
		} else {
			emit := etag
			weak := false
			if len(emit) >= 2 && (emit[0] == 'W' || emit[0] == 'w') && emit[1] == '/' {
				weak = true
				emit = emit[2:]
			}
			if len(emit) < 2 || emit[0] != '"' || emit[len(emit)-1] != '"' {
				emit = `"` + emit + `"`
			}
			if weak {
				emit = "W/" + emit
			}
			etag = emit
			w.Header().Set("ETag", etag)

			if inm := strings.Join(r.Header.Values("If-None-Match"), ","); inm != "" && etagMatch(inm, etag) {
				w.WriteHeader(http.StatusNotModified)
				return nil
			}
		}
	}

	// Body needed: open via Get when we only Stat'ed above.
	if rc == nil {
		rc, meta, err = backend.Get(r.Context(), key)
		if err != nil {
			return err
		}
		defer func() { _ = rc.Close() }()
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

// etagMatch checks if serverETag matches any ETag in the If-None-Match header value.
// Handles the W/ (weak) prefix per RFC 7232 §2.3: weak comparison ignores the prefix.
// The inm value may contain multiple comma-separated ETags (e.g., `"a", W/"b"`).
func etagMatch(inm, serverETag string) bool {
	if inm == "*" {
		return true
	}
	// Strip W/ prefix for weak comparison (RFC 7232 §2.3.2).
	serverStrong := stripWeakPrefix(serverETag)

	for inm != "" {
		// Skip leading whitespace and commas.
		for len(inm) > 0 && (inm[0] == ' ' || inm[0] == ',') {
			inm = inm[1:]
		}
		if inm == "" {
			break
		}
		candidate := stripWeakPrefix(inm)
		// Find the closing quote. Malformed tokens are skipped so a later
		// well-formed match still yields 304 (RFC 7232 §3.2 allows lists).
		if len(candidate) < 2 || candidate[0] != '"' {
			// Advance past this token to the next comma/end.
			i := 0
			for i < len(inm) && inm[i] != ',' {
				i++
			}
			inm = inm[i:]
			continue
		}
		end := 1
		for end < len(candidate) && candidate[end] != '"' {
			end++
		}
		if end >= len(candidate) {
			i := 0
			for i < len(inm) && inm[i] != ',' {
				i++
			}
			inm = inm[i:]
			continue
		}
		tag := candidate[:end+1]
		if tag == serverStrong {
			return true
		}
		// Advance past this tag in the original inm.
		consumed := len(inm) - len(candidate) + end + 1
		inm = inm[consumed:]
	}
	return false
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
