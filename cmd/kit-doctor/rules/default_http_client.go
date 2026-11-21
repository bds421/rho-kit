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

// kitClientSuggestion is the fix the rule suggests for every finding but
// the package helpers'.
const kitClientSuggestion = "use httpx.NewHTTPClient(...) or a kit helper that supplies timeouts and transport policy"

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
	findings := highFindings(fset, file, r.Name(), func(n ast.Node) (flaggedUse, bool) {
		return defaultClientUse(n, httpAliases)
	})
	return append(findings, highFindings(fset, file, r.Name(), func(n ast.Node) (flaggedUse, bool) {
		return clientConstruction(n, httpAliases)
	})...)
}

// defaultClientUse reports a use of http.DefaultClient or
// http.DefaultTransport at n.
func defaultClientUse(n ast.Node, httpAliases map[string]struct{}) (flaggedUse, bool) {
	sel, ok := n.(*ast.SelectorExpr)
	if !ok {
		return flaggedUse{}, false
	}
	ident, ok := sel.X.(*ast.Ident)
	if !ok {
		return flaggedUse{}, false
	}
	if _, ok := httpAliases[ident.Name]; !ok {
		return flaggedUse{}, false
	}
	switch sel.Sel.Name {
	case "DefaultClient", "DefaultTransport":
		if !isPackageAlias(ident, httpAliases) {
			return flaggedUse{}, false
		}
		return flaggedUse{sel.Pos(), "direct use of http." + sel.Sel.Name + " (no timeout, no SSRF guard)", kitClientSuggestion}, true
	}
	return flaggedUse{}, false
}

// clientConstruction reports an http.Client built outside the kit at n:
// a zero-value variable, a composite literal, new(http.Client), or a
// package helper such as http.Get that uses http.DefaultClient.
func clientConstruction(n ast.Node, httpAliases map[string]struct{}) (flaggedUse, bool) {
	switch node := n.(type) {
	case *ast.ValueSpec:
		if isHTTPClientSelector(node.Type, httpAliases) {
			return flaggedUse{node.Type.Pos(), "zero-value http.Client bypasses kit timeout and transport defaults", kitClientSuggestion}, true
		}
	case *ast.CompositeLit:
		if isHTTPClientSelector(node.Type, httpAliases) {
			return flaggedUse{node.Pos(), "direct construction of http.Client bypasses kit timeout and transport defaults", kitClientSuggestion}, true
		}
	case *ast.CallExpr:
		if isNewHTTPClient(node, httpAliases) {
			return flaggedUse{node.Pos(), "new(http.Client) bypasses kit timeout and transport defaults", kitClientSuggestion}, true
		}
		if helper, ok := httpClientHelper(node.Fun, httpAliases); ok {
			return flaggedUse{node.Pos(), helper + " uses http.DefaultClient and bypasses kit timeout and transport defaults",
				"use a client from httpx.NewHTTPClient(...) and call its Do method"}, true
		}
	}
	return flaggedUse{}, false
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
