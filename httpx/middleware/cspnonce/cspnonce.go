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
// style-src directives of the configured base policy on every request,
// and stores the raw nonce string in the request context for the
// handler/templates to consume.
package cspnonce

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"html/template"
	"net/http"
	"strings"
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

// Option configures the middleware.
type Option func(*config)

type config struct {
	basePolicy string
	headerName string // "Content-Security-Policy" or "Content-Security-Policy-Report-Only"
}

// WithBasePolicy overrides the base CSP policy. The middleware augments
// the script-src and style-src directives (creating them if absent) so
// the per-request nonce is honoured. Other directives pass through
// unchanged.
//
// Default: "default-src 'self'; object-src 'none'; base-uri 'self'; frame-ancestors 'none'".
func WithBasePolicy(policy string) Option {
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
		o(&cfg)
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			nonce, err := generateNonce()
			if err != nil {
				// Fail closed: a missing nonce means the response would
				// either trip the CSP or render with weakened policy.
				http.Error(w, "csp nonce generation failed", http.StatusInternalServerError)
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
	// Nonce is base64 (URL-safe alphabet + '=' padding); no HTML
	// escape required, but be explicit about the trust boundary.
	return template.HTMLAttr(`nonce="` + n + `"`)
}

func generateNonce() (string, error) {
	buf := make([]byte, nonceLenBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("read nonce: %w", err)
	}
	return base64.RawStdEncoding.EncodeToString(buf), nil
}

// injectNonce splices `'nonce-<n>'` into the script-src and style-src
// directives of policy. If a directive is absent, it is added with
// 'self' as the source list plus the nonce. Other directives pass
// through unchanged.
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
		hadScriptSrc bool
		hadStyleSrc  bool
	)
	for i, raw := range parts {
		d := strings.TrimSpace(raw)
		if d == "" {
			continue
		}
		name, _, _ := strings.Cut(d, " ")
		switch strings.ToLower(name) {
		case "script-src":
			parts[i] = " " + d + " 'nonce-" + nonce + "'"
			hadScriptSrc = true
		case "style-src":
			parts[i] = " " + d + " 'nonce-" + nonce + "'"
			hadStyleSrc = true
		}
	}
	if !hadScriptSrc {
		parts = append(parts, " script-src 'self' 'nonce-"+nonce+"'")
	}
	if !hadStyleSrc {
		parts = append(parts, " style-src 'self' 'nonce-"+nonce+"'")
	}
	return strings.TrimSpace(strings.Join(parts, ";"))
}
