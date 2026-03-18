package storagehttp

import (
	"io"
	"log/slog"
	"mime"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/bds421/rho-kit/infra/storage"
)

// ServeOptions configures [ServeFile] behavior.
type ServeOptions struct {
	// ContentDisposition controls the Content-Disposition header.
	// "inline" (default) displays in-browser; "attachment" forces download.
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
// Note: in the streaming fallback path, if io.Copy fails after
// WriteHeader(200) has been sent, the HTTP status code cannot be changed.
// The returned error should be logged but cannot be translated into an
// HTTP error response at that point.
func ServeFile(w http.ResponseWriter, r *http.Request, backend storage.Storage, key string, opts ServeOptions) error {
	if err := storage.ValidateKey(key); err != nil {
		return err
	}

	rc, meta, err := backend.Get(r.Context(), key)
	if err != nil {
		return err
	}
	defer func() { _ = rc.Close() }()

	filename := opts.Filename
	if filename == "" {
		filename = path.Base(key)
	}

	contentType := meta.ContentType
	if contentType == "" {
		contentType = mime.TypeByExtension(path.Ext(filename))
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	// Restrict Content-Disposition to a safe allowlist to prevent header injection.
	disposition := opts.ContentDisposition
	if disposition != "inline" && disposition != "attachment" {
		disposition = "inline"
	}

	w.Header().Set("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{
		"filename": filename,
	}))

	if opts.CacheControl != "" {
		w.Header().Set("Cache-Control", opts.CacheControl)
	}

	// Set ETag for conditional-GET support.
	if meta.ETag != "" {
		etag := meta.ETag
		// Reject ETags containing control characters (CR, LF, NUL) or internal
		// double quotes to prevent HTTP header injection and malformed ETags.
		// Per RFC 7232 §2.3, etagc excludes DQUOTE: ETags are DQUOTE *etagc DQUOTE.
		// While Go's net/http sanitizes headers since 1.17, defense-in-depth
		// requires validating at the application layer.
		stripped := etag
		if len(stripped) >= 2 && stripped[0] == '"' && stripped[len(stripped)-1] == '"' {
			stripped = stripped[1 : len(stripped)-1]
		}
		if strings.ContainsAny(stripped, "\r\n\x00\"") {
			slog.Warn("storagehttp: ETag contains invalid characters, skipping",
				"key", key)
		} else {
			// Ensure ETag is quoted per RFC 7232.
			if len(etag) < 2 || etag[0] != '"' || etag[len(etag)-1] != '"' {
				etag = `"` + etag + `"`
			}
			w.Header().Set("ETag", etag)

			// Check If-None-Match for conditional-GET (304 Not Modified).
			// Handles the common single-ETag case and the multi-value/W/ prefix cases.
			if inm := r.Header.Get("If-None-Match"); inm != "" && etagMatch(inm, etag) {
				w.WriteHeader(http.StatusNotModified)
				return nil
			}
		}
	}

	// If the reader supports seeking, use http.ServeContent for Range support.
	// http.ServeContent handles If-Modified-Since and Range internally.
	if rs, ok := rc.(io.ReadSeeker); ok {
		w.Header().Set("Content-Type", contentType)
		modTime := meta.LastModified
		if modTime.IsZero() {
			modTime = time.Time{}
		}
		http.ServeContent(w, r, filename, modTime, rs)
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
		// Find the closing quote.
		if len(candidate) < 2 || candidate[0] != '"' {
			break // malformed
		}
		end := 1
		for end < len(candidate) && candidate[end] != '"' {
			end++
		}
		if end >= len(candidate) {
			break // no closing quote
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
