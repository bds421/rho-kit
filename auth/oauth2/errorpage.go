package oauth2

import (
	"encoding/base64"
	"html/template"
	"net/http"
)

// ErrorPageBrand names and draws the product on the page a browser sees when
// a sign-in cannot be completed.
type ErrorPageBrand struct {
	// Name is the product name shown above the message.
	Name string
	// LogoSVG is an SVG document drawn above the name; empty for none.
	LogoSVG []byte
	// Accent is the CSS color of the "Sign in again" button.
	Accent string
}

// WithErrorPageBrand brands the sign-in error page. Without it the page shows
// a neutral look. The page is translated (English, German, Spanish) from the
// browser's Accept-Language.
func WithErrorPageBrand(b ErrorPageBrand) Option {
	return func(c *Client) { c.errorBrand = b }
}

type failure int

const (
	failureSignIn failure = iota
	failureRefused
	failureUnavailable
)

type errorPageData struct {
	Lang    string
	Title   string
	Message string
	Action  string
	Retry   string
	Name    string
	Logo    template.URL
	Accent  string
}

// errorPagePolicy confines the page to its own inline styles and the inline
// logo, whatever policy the surrounding application sets.
const errorPagePolicy = "default-src 'none'; style-src 'unsafe-inline'; img-src data:; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"

// renderFailure answers a sign-in that cannot be completed with a translated
// page offering to sign in again, never with the internal error text.
func (c *Client) renderFailure(w http.ResponseWriter, r *http.Request, status int, f failure, returnTo string) {
	text := errorTexts[preferredLanguage(r.Header.Get("Accept-Language"))]
	data := errorPageData{
		Lang:    text.lang,
		Title:   text.title,
		Message: text.messages[f],
		Action:  text.action,
		Retry:   c.loginURL(c.safeReturnTo(returnTo)),
		Name:    c.errorBrand.Name,
		Accent:  c.errorBrand.Accent,
	}
	if data.Accent == "" {
		data.Accent = "#2563eb"
	}
	if len(c.errorBrand.LogoSVG) > 0 {
		data.Logo = template.URL("data:image/svg+xml;base64," + base64.StdEncoding.EncodeToString(c.errorBrand.LogoSVG))
	}
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Content-Security-Policy", errorPagePolicy)
	h.Set("Cache-Control", "no-store")
	h.Set("Content-Language", text.lang)
	w.WriteHeader(status)
	if err := errorPage.Execute(w, data); err != nil {
		c.logger.WarnContext(r.Context(), "oauth2: error page render failed")
	}
}

var errorPage = template.Must(template.New("error").Parse(`<!doctype html>
<html lang="{{.Lang}}">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="color-scheme" content="light dark">
<title>{{.Title}}{{if .Name}} · {{.Name}}{{end}}</title>
<style>
:root{--bg:#f6f7f9;--card:#fff;--text:#111827;--muted:#4b5563;--border:#e5e7eb;--accent:{{.Accent}}}
@media (prefers-color-scheme:dark){:root{--bg:#0b0d12;--card:#151821;--text:#f3f4f6;--muted:#9ca3af;--border:#262a36}}
*{box-sizing:border-box}
body{margin:0;min-height:100vh;display:grid;place-items:center;padding:16px;background:var(--bg);color:var(--text);font:16px/1.5 Inter,ui-sans-serif,system-ui,-apple-system,"Segoe UI",Roboto,sans-serif}
main{width:100%;max-width:420px;background:var(--card);border:1px solid var(--border);border-radius:16px;padding:32px;text-align:center}
.brand{display:flex;flex-direction:column;align-items:center;gap:8px;margin-bottom:24px;font-weight:600;color:var(--muted)}
.brand img{height:40px;width:auto}
h1{font-size:20px;line-height:1.3;margin:0 0 8px}
p{margin:0 0 24px;color:var(--muted)}
a{display:inline-block;padding:10px 20px;border-radius:10px;background:var(--accent);color:#fff;text-decoration:none;font-weight:600}
a:focus-visible{outline:3px solid var(--accent);outline-offset:3px}
</style>
</head>
<body>
<main>
{{if or .Logo .Name}}<div class="brand">{{if .Logo}}<img src="{{.Logo}}" alt="">{{end}}{{if .Name}}<span>{{.Name}}</span>{{end}}</div>{{end}}
<h1>{{.Title}}</h1>
<p>{{.Message}}</p>
<a href="{{.Retry}}">{{.Action}}</a>
</main>
</body>
</html>
`))
