package rules

import (
	"go/ast"
	"go/token"
	"strings"
)

// defaultHTTPClientRule flags direct use of net/http client defaults:
// `http.DefaultClient`, `http.DefaultTransport`, package helpers such as
// `http.Get`, and direct `http.Client` construction. These have no kit
// timeout/TLS/redirect/SSRF policy by default — exactly the pattern the kit's
// `httpx.NewHTTPClient` exists to prevent.
type defaultHTTPClientRule struct{}

func (defaultHTTPClientRule) Name() string { return "default-http-client" }

func (r defaultHTTPClientRule) Run(fset *token.FileSet, file *ast.File) []Finding {
	if file == nil {
		return nil
	}
	// Tests routinely swap http.DefaultTransport to assert the kit
	// helpers stay panic-free under custom RoundTrippers; that is not
	// the production-wiring path this rule guards.
	if strings.HasSuffix(fset.Position(file.Pos()).Filename, "_test.go") {
		return nil
	}
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
		switch sel.Sel.Name {
		case "DefaultClient", "DefaultTransport":
			if !isPackageAlias(ident, httpAliases) {
				return true
			}
			pos := fset.Position(sel.Pos())
			if isExempt(fset, file, r.Name(), pos.Filename, pos.Line) {
				return true
			}
			findings = append(findings, Finding{
				Rule:       r.Name(),
				Severity:   High,
				File:       pos.Filename,
				Line:       pos.Line,
				Message:    "direct use of http." + sel.Sel.Name + " (no timeout, no SSRF guard)",
				Suggestion: "use httpx.NewHTTPClient(...) or a kit helper that supplies timeouts and transport policy",
			})
		}
		return true
	})
	ast.Inspect(file, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.ValueSpec:
			if !isHTTPClientSelector(node.Type, httpAliases) {
				return true
			}
			pos := fset.Position(node.Type.Pos())
			if isExempt(fset, file, r.Name(), pos.Filename, pos.Line) {
				return true
			}
			findings = append(findings, Finding{
				Rule:       r.Name(),
				Severity:   High,
				File:       pos.Filename,
				Line:       pos.Line,
				Message:    "zero-value http.Client bypasses kit timeout and transport defaults",
				Suggestion: "use httpx.NewHTTPClient(...) or a kit helper that supplies timeouts and transport policy",
			})
		case *ast.CompositeLit:
			if !isHTTPClientSelector(node.Type, httpAliases) {
				return true
			}
			pos := fset.Position(node.Pos())
			if isExempt(fset, file, r.Name(), pos.Filename, pos.Line) {
				return true
			}
			findings = append(findings, Finding{
				Rule:       r.Name(),
				Severity:   High,
				File:       pos.Filename,
				Line:       pos.Line,
				Message:    "direct construction of http.Client bypasses kit timeout and transport defaults",
				Suggestion: "use httpx.NewHTTPClient(...) or a kit helper that supplies timeouts and transport policy",
			})
		case *ast.CallExpr:
			if isNewHTTPClient(node, httpAliases) {
				pos := fset.Position(node.Pos())
				if isExempt(fset, file, r.Name(), pos.Filename, pos.Line) {
					return true
				}
				findings = append(findings, Finding{
					Rule:       r.Name(),
					Severity:   High,
					File:       pos.Filename,
					Line:       pos.Line,
					Message:    "new(http.Client) bypasses kit timeout and transport defaults",
					Suggestion: "use httpx.NewHTTPClient(...) or a kit helper that supplies timeouts and transport policy",
				})
				return true
			}
			if helper, ok := httpClientHelper(node.Fun, httpAliases); ok {
				pos := fset.Position(node.Pos())
				if isExempt(fset, file, r.Name(), pos.Filename, pos.Line) {
					return true
				}
				findings = append(findings, Finding{
					Rule:       r.Name(),
					Severity:   High,
					File:       pos.Filename,
					Line:       pos.Line,
					Message:    helper + " uses http.DefaultClient and bypasses kit timeout and transport defaults",
					Suggestion: "use a client from httpx.NewHTTPClient(...) and call its Do method",
				})
			}
		}
		return true
	})
	return findings
}

func isHTTPClientSelector(expr ast.Expr, httpAliases map[string]struct{}) bool {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok || sel.Sel == nil || sel.Sel.Name != "Client" {
		return false
	}
	ident, ok := sel.X.(*ast.Ident)
	return ok && isPackageAlias(ident, httpAliases)
}

func isNewHTTPClient(call *ast.CallExpr, httpAliases map[string]struct{}) bool {
	ident, ok := call.Fun.(*ast.Ident)
	if !ok || ident.Name != "new" || len(call.Args) != 1 {
		return false
	}
	return isHTTPClientSelector(call.Args[0], httpAliases)
}

func httpClientHelper(expr ast.Expr, httpAliases map[string]struct{}) (string, bool) {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok || sel.Sel == nil {
		return "", false
	}
	switch sel.Sel.Name {
	case "Get", "Head", "Post", "PostForm":
	default:
		return "", false
	}
	ident, ok := sel.X.(*ast.Ident)
	if !ok || !isPackageAlias(ident, httpAliases) {
		return "", false
	}
	return ident.Name + "." + sel.Sel.Name, true
}
