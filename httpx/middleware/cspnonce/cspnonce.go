// Package cspnonce supplies a per-request CSP nonce middleware suitable
// for HTML-rendering services that need inline <script>/<style> blocks
// without weakening the Content-Security-Policy with 'unsafe-inline'.
//
// Usage:
//
//	mux.Handle("/admin", cspnonce.Middleware(
//	    cspnonce.WithBasePolicy("default-src 'self'; object-src 'none'"),
//	)(adminHandler))
//
//	// In the handler:
//	nonce := cspnonce.FromContext(r.Context())
//	tmpl.Execute(w, map[string]any{"Nonce": nonce})
//
//	// In the template:
//	<script nonce="{{ .Nonce }}">…</script>
//
// The middleware injects 'nonce-<value>' into the script-src and
// style-src directives (and their script-src-elem/style-src-elem
// variants, when present) of the configured base policy on every
// request, and stores the raw nonce string in the request context for
// the handler/templates to consume. When script-src/style-src must be
// created, it inherits the default-src source list so a stricter base
// policy is not silently widened.
//
// asvs: V9.2.1, V14.4.1
package cspnonce

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"strings"

	"github.com/bds421/rho-kit/httpx/v2"
)

type ctxKey struct{}

// nonceLenBytes is the per-request entropy. 16 bytes (128 bits) is the
// W3C-recommended minimum; base64 produces a 22-char string.
const nonceLenBytes = 16

// defaultBasePolicy is the policy applied when [WithBasePolicy] is not
// supplied. Conservative-by-default: nothing loads from anywhere except
// the same origin, except for inline script/style which are gated on
// the per-request nonce.
const defaultBasePolicy = "default-src 'self'; object-src 'none'; base-uri 'self'; frame-ancestors 'none'"

var nonceRandReader io.Reader = rand.Reader

// Option configures the middleware.
type Option func(*config)

type config struct {
	basePolicy string
	headerName string // "Content-Security-Policy" or "Content-Security-Policy-Report-Only"
}

// WithBasePolicy overrides the base CSP policy. The middleware augments
// the script-src and style-src directives — and their
// script-src-elem/style-src-elem variants when the base policy declares
// them — so the per-request nonce is honoured. Absent script-src/style-src
// are created, inheriting the default-src source list so a stricter base
// policy (e.g. "default-src 'none'") is not silently widened. Other
// directives pass through unchanged.
//
// Default: "default-src 'self'; object-src 'none'; base-uri 'self'; frame-ancestors 'none'".
func WithBasePolicy(policy string) Option {
	validateHeaderValue("Content-Security-Policy", policy)
	return func(c *config) { c.basePolicy = policy }
}

// WithReportOnly switches the middleware to emit
// `Content-Security-Policy-Report-Only` instead of the enforcing header.
// Useful for staged rollout: deploy the policy in report-only mode,
// review violations, then flip to enforcing.
func WithReportOnly() Option {
	return func(c *config) { c.headerName = "Content-Security-Policy-Report-Only" }
}

// Middleware returns the per-request CSP-nonce middleware.
func Middleware(opts ...Option) func(http.Handler) http.Handler {
	cfg := config{
		basePolicy: defaultBasePolicy,
		headerName: "Content-Security-Policy",
	}
	for _, o := range opts {
		if o == nil {
			panic("cspnonce: Middleware option must not be nil")
		}
		o(&cfg)
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			nonce, err := generateNonce()
			if err != nil {
				// Fail closed: a missing nonce means the response would
				// either trip the CSP or render with weakened policy.
				httpx.WriteError(w, http.StatusInternalServerError, "csp nonce generation failed")
				return
			}

			policy := injectNonce(cfg.basePolicy, nonce)
			w.Header().Set(cfg.headerName, policy)

			ctx := context.WithValue(r.Context(), ctxKey{}, nonce)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// FromContext returns the per-request nonce, or "" if the middleware
// was not installed for this request.
func FromContext(ctx context.Context) string {
	v, _ := ctx.Value(ctxKey{}).(string)
	return v
}

// HTMLAttr returns the per-request nonce wrapped in
// `nonce="..."` ready to splice into a template attribute. Empty
// string when the middleware wasn't installed.
//
//	t := template.New("page").Funcs(template.FuncMap{
//	    "cspnonce": func() template.HTMLAttr { return cspnonce.HTMLAttr(r.Context()) },
//	})
//	// {{ cspnonce }} in template renders nonce="<value>".
func HTMLAttr(ctx context.Context) template.HTMLAttr {
	n := FromContext(ctx)
	if n == "" {
		return ""
	}
	// Nonce is base64 RawStdEncoding (standard alphabet '+'/'/', no
	// padding); none of those characters need HTML escaping, but be
	// explicit about the trust boundary.
	return template.HTMLAttr(`nonce="` + n + `"`)
}

func generateNonce() (string, error) {
	buf := make([]byte, nonceLenBytes)
	if _, err := io.ReadFull(nonceRandReader, buf); err != nil {
		return "", fmt.Errorf("read nonce: %w", err)
	}
	return base64.RawStdEncoding.EncodeToString(buf), nil
}

// injectNonce splices `'nonce-<n>'` into the script-src/style-src and
// their *-elem variants of policy. If script-src/style-src is absent,
// it is added, inheriting the default-src source list (so a stricter
// base policy such as "default-src 'none'" is not silently widened),
// plus the nonce. Other directives pass through unchanged.
//
// The *-elem directives (script-src-elem/style-src-elem) take
// precedence over script-src/style-src for element-level enforcement,
// so the nonce is injected into them too when the base policy declares
// them; otherwise nonced inline elements would stay blocked.
//
// The implementation is intentionally simple: split on ';', augment
// the recognized directives, rejoin. Conformant CSP grammar — no
// escaped semicolons, no quoted values containing ';'.
func injectNonce(policy, nonce string) string {
	if policy == "" {
		return "script-src 'self' 'nonce-" + nonce + "'; style-src 'self' 'nonce-" + nonce + "'"
	}
	parts := strings.Split(policy, ";")
	var (
		hadScriptSrc  bool
		hadStyleSrc   bool
		hadDefaultSrc bool
		defaultSrc    string // inheritable source list of default-src
	)
	for i, raw := range parts {
		d := strings.TrimSpace(raw)
		if d == "" {
			continue
		}
		name := directiveName(d)
		switch strings.ToLower(name) {
		case "script-src", "script-src-elem":
			parts[i] = " " + d + " 'nonce-" + nonce + "'"
			if strings.EqualFold(name, "script-src") {
				hadScriptSrc = true
			}
		case "style-src", "style-src-elem":
			parts[i] = " " + d + " 'nonce-" + nonce + "'"
			if strings.EqualFold(name, "style-src") {
				hadStyleSrc = true
			}
		case "default-src":
			hadDefaultSrc = true
			defaultSrc = inheritableSourceList(d)
		}
	}
	if !hadScriptSrc {
		parts = append(parts, " "+newNonceDirective("script-src", hadDefaultSrc, defaultSrc, nonce))
	}
	if !hadStyleSrc {
		parts = append(parts, " "+newNonceDirective("style-src", hadDefaultSrc, defaultSrc, nonce))
	}
	return strings.TrimSpace(strings.Join(parts, ";"))
}

// inheritableSourceList returns the source list of a default-src
// directive (the tokens after the directive name), suitable for
// reuse on an inherited script-src/style-src. A bare 'none' yields the
// empty string: 'none' is not composable with other sources, so the
// inherited directive must carry only the nonce.
func inheritableSourceList(defaultSrcDirective string) string {
	fields := strings.Fields(defaultSrcDirective)
	if len(fields) <= 1 {
		return ""
	}
	sources := fields[1:]
	if len(sources) == 1 && strings.EqualFold(sources[0], "'none'") {
		return ""
	}
	return strings.Join(sources, " ")
}

// newNonceDirective builds a directive that did not exist in the base
// policy, plus the per-request nonce. When default-src is present, the
// new directive inherits its (composable) source list so the operator's
// intent is preserved and a stricter policy is not widened — including
// the case where default-src is 'none', which inherits an empty source
// list (nonce-only). When no default-src was present at all, it falls
// back to 'self' (the historical default).
func newNonceDirective(name string, hadDefaultSrc bool, defaultSrc, nonce string) string {
	nonceToken := "'nonce-" + nonce + "'"
	switch {
	case hadDefaultSrc && defaultSrc != "":
		return name + " " + defaultSrc + " " + nonceToken
	case hadDefaultSrc:
		// default-src present but yields no composable source (e.g. 'none').
		return name + " " + nonceToken
	default:
		return name + " 'self' " + nonceToken
	}
}

func directiveName(d string) string {
	fields := strings.Fields(d)
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

func validateHeaderValue(name, value string) {
	if value != "" && strings.TrimSpace(value) != value {
		panic("cspnonce: header value contains leading or trailing whitespace")
	}
	for i := 0; i < len(value); i++ {
		switch value[i] {
		case 0, '\r', '\n':
			panic("cspnonce: header value is invalid")
		}
	}
}
