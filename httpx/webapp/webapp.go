// Package webapp serves a built single-page web app, such as a Vite build
// embedded with go:embed.
//
// A request for a file of the build gets that file; every other path gets
// index.html, so links into the app work, except a path that names a file
// (its last segment has an extension, or a segment starts with a dot), which
// answers 404. Files and directories whose names start with a dot are not
// served unless allowed: ".well-known/" always is, more with [WithDotPaths].
// A file whose extension has no known type is sent as
// application/octet-stream, never sniffed. Files with a precompressed sibling
// (app.js.br, app.js.gz) are sent in the coding the client accepts, with
// Content-Encoding and Vary set. Paths under an immutable prefix
// ([WithImmutable]) carry a content hash in their names: they are cached for
// a year, and a missing one answers 404 instead of the app, so a stale tab
// fails loudly rather than parsing HTML as a script. Everything else, index.html
// included, is revalidated on every use through its ETag.
//
// Mount the handler behind [stack.WithStaticPaths] for the immutable prefix:
// the timeout middleware buffers each response up to 1 MiB, and large script
// files do not belong in that buffer.
package webapp

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/bds421/rho-kit/httpx/v2/internal/acceptencoding"
)

const (
	indexFile      = "index.html"
	immutableCache = "public, max-age=31536000, immutable"
	revalidate     = "no-cache"
)

// codings lists the precompressed siblings in the order the server prefers
// them when the client accepts both equally.
var codings = []struct{ name, suffix string }{
	{"br", ".br"},
	{"gzip", ".gz"},
}

// Option configures [New].
type Option func(*config)

type config struct {
	immutable []string
	dotPaths  []string
}

// wellKnown is public by definition (RFC 8615).
const wellKnown = ".well-known/"

// WithDotPaths allows files under names that start with a dot. Each entry is
// a file name (".well-known/apple-app-site-association") or a directory
// prefix ending in "/". ".well-known/" is always allowed.
func WithDotPaths(paths ...string) Option {
	for _, p := range paths {
		if strings.Trim(p, "/") == "" {
			panic("webapp: WithDotPaths requires a non-empty path")
		}
	}
	return func(c *config) {
		for _, p := range paths {
			c.dotPaths = append(c.dotPaths, strings.TrimPrefix(p, "/"))
		}
	}
}

// WithImmutable names path prefixes (such as "assets/") whose file names
// carry a content hash. Panics on an empty prefix, which would make every
// path immutable.
func WithImmutable(prefixes ...string) Option {
	for _, p := range prefixes {
		if strings.Trim(p, "/") == "" {
			panic("webapp: WithImmutable requires a non-empty prefix")
		}
	}
	return func(c *config) {
		for _, p := range prefixes {
			c.immutable = append(c.immutable, strings.TrimPrefix(p, "/"))
		}
	}
}

type variant struct {
	file   string
	coding string
	etag   string
}

type file struct {
	contentType string
	variants    []variant // precompressed first, the plain file last
}

type handler struct {
	files     fs.FS
	byName    map[string]*file
	index     *file
	immutable []string
}

// New reads the build once to index its files and their precompressed
// siblings. It fails when the build has no index.html.
func New(files fs.FS, opts ...Option) (http.Handler, error) {
	var cfg config
	for _, opt := range opts {
		if opt == nil {
			panic("webapp: New option must not be nil")
		}
		opt(&cfg)
	}
	h := &handler{files: files, byName: make(map[string]*file), immutable: cfg.immutable}
	names, err := regularFiles(files, append(cfg.dotPaths, wellKnown))
	if err != nil {
		return nil, err
	}
	for name := range names {
		if isSibling(name, names) {
			continue
		}
		f, err := index(files, name, names)
		if err != nil {
			return nil, err
		}
		h.byName[name] = f
	}
	h.index = h.byName[indexFile]
	if h.index == nil {
		return nil, errors.New("webapp: the build has no index.html")
	}
	return h, nil
}

func regularFiles(files fs.FS, dotPaths []string) (map[string]bool, error) {
	names := make(map[string]bool)
	err := fs.WalkDir(files, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type().IsRegular() && (!hasDotSegment(name) || allowed(name, dotPaths)) {
			names[name] = true
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("webapp: read build: %w", err)
	}
	return names, nil
}

func allowed(name string, dotPaths []string) bool {
	for _, p := range dotPaths {
		if name == p || (strings.HasSuffix(p, "/") && strings.HasPrefix(name, p)) {
			return true
		}
	}
	return false
}

func hasDotSegment(name string) bool {
	for segment := range strings.SplitSeq(name, "/") {
		if strings.HasPrefix(segment, ".") {
			return true
		}
	}
	return false
}

func namesFile(name string) bool {
	return hasDotSegment(name) || path.Ext(name) != ""
}

func isSibling(name string, names map[string]bool) bool {
	for _, c := range codings {
		if base, ok := strings.CutSuffix(name, c.suffix); ok && names[base] {
			return true
		}
	}
	return false
}

func index(files fs.FS, name string, names map[string]bool) (*file, error) {
	f := &file{contentType: mime.TypeByExtension(path.Ext(name))}
	for _, c := range codings {
		if names[name+c.suffix] {
			v, err := newVariant(files, name+c.suffix, c.name)
			if err != nil {
				return nil, err
			}
			f.variants = append(f.variants, v)
		}
	}
	plain, err := newVariant(files, name, "")
	if err != nil {
		return nil, err
	}
	f.variants = append(f.variants, plain)
	if f.contentType == "" {
		f.contentType = "application/octet-stream"
	}
	return f, nil
}

func newVariant(files fs.FS, name, coding string) (variant, error) {
	data, err := fs.ReadFile(files, name)
	if err != nil {
		return variant{}, fmt.Errorf("webapp: read %s: %w", name, err)
	}
	sum := sha256.Sum256(data)
	return variant{file: name, coding: coding, etag: `"` + hex.EncodeToString(sum[:16]) + `"`}, nil
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
		return
	}
	if containsDotDot(r.URL.Path) {
		http.Error(w, "invalid URL path", http.StatusBadRequest)
		return
	}
	name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
	immutable := h.isImmutable(name)
	f, found := h.byName[name]
	switch {
	case found && name != indexFile && immutable:
		h.serve(w, r, f, immutableCache)
	case found && name != indexFile:
		h.serve(w, r, f, revalidate)
	case immutable, name != indexFile && namesFile(name):
		http.NotFound(w, r)
	default:
		h.serve(w, r, h.index, revalidate)
	}
}

func (h *handler) isImmutable(name string) bool {
	for _, p := range h.immutable {
		if strings.HasPrefix(name+"/", p) {
			return true
		}
	}
	return false
}

func (h *handler) serve(w http.ResponseWriter, r *http.Request, f *file, cacheControl string) {
	v := f.choose(strings.Join(r.Header.Values("Accept-Encoding"), ","))
	content, err := h.open(v.file)
	if err != nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	if c, ok := content.(io.Closer); ok {
		defer func() { _ = c.Close() }()
	}
	header := w.Header()
	header.Set("Cache-Control", cacheControl)
	header.Set("ETag", v.etag)
	if len(f.variants) > 1 {
		header.Add("Vary", "Accept-Encoding")
	}
	header.Set("Content-Type", f.contentType)
	if v.coding != "" {
		header.Set("Content-Encoding", v.coding)
	}
	http.ServeContent(w, r, "", time.Time{}, content)
}

func (f *file) choose(acceptEncoding string) variant {
	offered := make([]string, 0, len(f.variants)-1)
	for _, v := range f.variants[:len(f.variants)-1] {
		offered = append(offered, v.coding)
	}
	chosen := acceptencoding.Select(acceptEncoding, offered)
	for _, v := range f.variants {
		if v.coding == chosen {
			return v
		}
	}
	return f.variants[len(f.variants)-1]
}

func (h *handler) open(name string) (io.ReadSeeker, error) {
	opened, err := h.files.Open(name)
	if err != nil {
		return nil, err
	}
	if seeker, ok := opened.(io.ReadSeeker); ok {
		return seeker, nil
	}
	defer func() { _ = opened.Close() }()
	data, err := io.ReadAll(opened)
	if err != nil {
		return nil, err
	}
	return bytes.NewReader(data), nil
}

func containsDotDot(p string) bool {
	if !strings.Contains(p, "..") {
		return false
	}
	for _, segment := range strings.FieldsFunc(p, func(r rune) bool { return r == '/' || r == '\\' }) {
		if segment == ".." {
			return true
		}
	}
	return false
}
