package rules

import (
	"go/ast"
	"go/token"
)

// defaultHTTPClientRule flags direct use of `http.DefaultClient` and
// `http.DefaultTransport`. These have no timeouts, no SSRF protection,
// and no observability hooks — exactly the pattern the kit's
// `httpx.NewClient` exists to prevent.
type defaultHTTPClientRule struct{}

func (defaultHTTPClientRule) Name() string { return "default-http-client" }

func (r defaultHTTPClientRule) Run(fset *token.FileSet, file *ast.File) []Finding {
	var findings []Finding
	ast.Inspect(file, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		ident, ok := sel.X.(*ast.Ident)
		if !ok || ident.Name != "http" {
			return true
		}
		switch sel.Sel.Name {
		case "DefaultClient", "DefaultTransport":
			pos := fset.Position(sel.Pos())
			findings = append(findings, Finding{
				Rule:       r.Name(),
				Severity:   High,
				File:       pos.Filename,
				Line:       pos.Line,
				Message:    "direct use of http." + sel.Sel.Name + " (no timeout, no SSRF guard)",
				Suggestion: "use httpx.NewClient(...) which supplies sane timeouts and lets you opt into SSRF protection",
			})
		}
		return true
	})
	return findings
}
