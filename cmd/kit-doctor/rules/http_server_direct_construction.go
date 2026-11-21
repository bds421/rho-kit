package rules

import (
	"go/ast"
	"go/token"
	"strings"
)

// httpServerDirectConstructionRule flags direct or implicit
// construction of `net/http.Server` (composite literal
// `&http.Server{...}`, `http.Server{...}`, `new(http.Server)`, or
// package helpers such as `http.ListenAndServe` and `http.Serve`).
// The kit's `httpx.NewServer` wires `ReadHeaderTimeout`,
// `ReadTimeout`, `WriteTimeout`, `IdleTimeout`, `MaxHeaderBytes`,
// and a structured slog-backed `ErrorLog`. Bypassing it leaves the
// server vulnerable to slowloris-style resource exhaustion and routes
// connection errors through the global `log` package.
//
// Test files are skipped: `httptest.NewServer` and one-off raw
// servers in tests are not the production-wiring path this rule
// guards.
type httpServerDirectConstructionRule struct{}

func (httpServerDirectConstructionRule) Name() string { return "http-server-direct-construction" }

func (r httpServerDirectConstructionRule) Run(fset *token.FileSet, file *ast.File) []Finding {
	if file == nil {
		return nil
	}
	pos := fset.Position(file.Pos())
	if strings.HasSuffix(pos.Filename, "_test.go") {
		return nil
	}
	httpAliases := importAliasesFor(file, "net/http")
	if len(httpAliases) == 0 {
		return nil
	}
	return highFindings(fset, file, r.Name(), func(n ast.Node) (flaggedUse, bool) {
		return r.serverConstruction(n, httpAliases)
	})
}

// serverConstruction reports an http.Server built outside the kit at n: a
// composite literal (`&http.Server{...}` is reported at its literal),
// new(http.Server), or a package helper such as http.ListenAndServe.
func (r httpServerDirectConstructionRule) serverConstruction(n ast.Node, httpAliases map[string]struct{}) (flaggedUse, bool) {
	const kitServerSuggestion = "use httpx.NewServer(addr, handler, opts...) which sets ReadHeaderTimeout, ReadTimeout, WriteTimeout, IdleTimeout, MaxHeaderBytes, and a slog-backed ErrorLog"
	switch node := n.(type) {
	case *ast.CompositeLit:
		if r.isHTTPServerSelector(node.Type, httpAliases) {
			return flaggedUse{node.Pos(), "direct construction of net/http.Server (no slowloris timeouts, default ErrorLog bypasses slog)", kitServerSuggestion}, true
		}
	case *ast.CallExpr:
		ident, ok := node.Fun.(*ast.Ident)
		if !ok || ident.Name != "new" || len(node.Args) != 1 {
			if helper, ok := r.httpServerHelper(node.Fun, httpAliases); ok {
				return flaggedUse{node.Pos(), helper + " bypasses kit server defaults (no slowloris timeouts, default ErrorLog bypasses slog)",
					"use httpx.NewServer(addr, handler, opts...) and call the returned server's ListenAndServe or Serve method"}, true
			}
			return flaggedUse{}, false
		}
		if r.isHTTPServerSelector(node.Args[0], httpAliases) {
			return flaggedUse{node.Pos(), "new(http.Server) bypasses kit defaults (no slowloris timeouts, default ErrorLog bypasses slog)", kitServerSuggestion}, true
		}
	}
	return flaggedUse{}, false
}

// isHTTPServerSelector reports whether expr is a SelectorExpr of the
// form <httpAlias>.Server, where <httpAlias> is one of the local
// identifiers that resolves to net/http.
func (httpServerDirectConstructionRule) isHTTPServerSelector(expr ast.Expr, httpAliases map[string]struct{}) bool {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	if sel.Sel == nil || sel.Sel.Name != "Server" {
		return false
	}
	ident, ok := sel.X.(*ast.Ident)
	if !ok {
		return false
	}
	if _, ok := httpAliases[ident.Name]; !ok {
		return false
	}
	if ident.Obj != nil && ident.Obj.Kind != ast.Pkg {
		return false
	}
	return true
}

func (httpServerDirectConstructionRule) httpServerHelper(expr ast.Expr, httpAliases map[string]struct{}) (string, bool) {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok || sel.Sel == nil {
		return "", false
	}
	switch sel.Sel.Name {
	case "ListenAndServe", "ListenAndServeTLS", "Serve", "ServeTLS":
	default:
		return "", false
	}
	ident, ok := sel.X.(*ast.Ident)
	if !ok {
		return "", false
	}
	if _, ok := httpAliases[ident.Name]; !ok {
		return "", false
	}
	if ident.Obj != nil && ident.Obj.Kind != ast.Pkg {
		return "", false
	}
	return ident.Name + "." + sel.Sel.Name, true
}
