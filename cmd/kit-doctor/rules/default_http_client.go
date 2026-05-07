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
	// Resolve which local identifiers actually refer to net/http so an
	// alias (`nethttp "net/http"`) is flagged and a local variable
	// named http is not.
	httpAliases := importAliasesFor(file, "net/http")
	if len(httpAliases) == 0 {
		return nil
	}
	var findings []Finding
	ast.Inspect(file, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		ident, ok := sel.X.(*ast.Ident)
		if !ok {
			return true
		}
		if _, ok := httpAliases[ident.Name]; !ok {
			return true
		}
		// Reject locally-shadowed identifiers: if this Ident resolves to
		// a non-package object in the file (a local var or param), skip.
		if ident.Obj != nil && ident.Obj.Kind != ast.Pkg {
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
