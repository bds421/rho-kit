package webapp

import (
	"bytes"
	"errors"
	"html"
	"io/fs"
	"net/http"

	"github.com/bds421/rho-kit/httpx/v2/middleware/cspnonce"
)

func (h *handler) renderPerRequest(files fs.FS) error {
	page := h.index.variants[len(h.index.variants)-1].data
	if page == nil {
		var err error
		if page, err = fs.ReadFile(files, indexFile); err != nil {
			return err
		}
	}
	if !bytes.Contains(page, []byte("</head>")) {
		return errors.New("webapp: WithCSPNonce requires </head> in index.html")
	}
	h.page = page
	return nil
}

// servePage answers index.html with the request's nonce; without one (the
// cspnonce middleware is not in front) the page goes out unchanged.
func (h *handler) servePage(w http.ResponseWriter, r *http.Request) {
	page := h.page
	if nonce := cspnonce.FromContext(r.Context()); nonce != "" {
		tag := []byte(`<meta property="csp-nonce" nonce="` + html.EscapeString(nonce) + `"></head>`)
		page = bytes.Replace(page, []byte("</head>"), tag, 1)
	}
	header := w.Header()
	header.Set("Cache-Control", "no-store")
	header.Set("Content-Type", h.index.contentType)
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(page)
}
