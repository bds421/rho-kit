package rules

import (
	"go/ast"
)

// isMethodCall reports whether call is a method call selector with
// the given selector name (e.g. recognises `b.WithJWT(...)` for
// "WithJWT" regardless of receiver expression shape).
func isMethodCall(call *ast.CallExpr, name string) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	return sel.Sel.Name == name
}

// isPackageCall reports whether call is a package-qualified call:
// `pkg.Func(...)` for the given package and function name.
func isPackageCall(call *ast.CallExpr, pkg, fn string) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	if sel.Sel.Name != fn {
		return false
	}
	ident, ok := sel.X.(*ast.Ident)
	if !ok {
		return false
	}
	return ident.Name == pkg
}

// chainRoot returns the outermost CallExpr that contains call's
// fluent chain. For `b.WithJWT(...).WithJWTIssuer(...)` invoked on
// `WithJWT`, this returns the `.WithJWTIssuer(...)` call so
// [chainHas] can scan all chained sites.
//
// Implementation note: this is a heuristic. The Go AST does not
// link chained calls upwards, so the recogniser walks the file
// instead in [chainHas]. We retain this signature for symmetry with
// future improvements that thread parent links through.
func chainRoot(call *ast.CallExpr) ast.Node {
	return call
}

// chainHas reports whether any of the named methods appear as
// selectors in the file enclosing root. It is intentionally
// over-approximating: any call to one of `names` anywhere in the
// surrounding file counts as the option being set, which means a
// rule won't flag callers who set the option in a sibling
// function in the same file. The trade-off is preferring false
// negatives over false positives — kit-doctor stays advisory.
func chainHas(root ast.Node, names ...string) bool {
	if root == nil {
		return false
	}
	want := make(map[string]struct{}, len(names))
	for _, n := range names {
		want[n] = struct{}{}
	}
	found := false
	ast.Inspect(rootFile(root), func(n ast.Node) bool {
		if found {
			return false
		}
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if _, ok := want[sel.Sel.Name]; ok {
			found = true
			return false
		}
		return true
	})
	return found
}

// rootFile returns the *ast.File that the rules engine attaches to
// nodes via the per-file scan. We track it via a package-level
// variable set in [SetCurrentFile]; rules are only ever invoked
// after SetCurrentFile.
func rootFile(_ ast.Node) ast.Node {
	if currentFile != nil {
		return currentFile
	}
	// Fall back to the node itself so the inspect over a single
	// expression at least runs.
	return nil
}

// currentFile is the file that the engine is currently scanning.
// Set via [SetCurrentFile] from the engine entry point.
var currentFile *ast.File

// SetCurrentFile is invoked by the engine before running each rule
// against a parsed file. Helpers that need to walk the surrounding
// file (e.g. fluent-chain detection) consult this state.
func SetCurrentFile(f *ast.File) {
	currentFile = f
}

// callHasOption reports whether call's argument list contains a
// selector whose name matches optName (e.g. detecting
// `httpx.WithErrorLog(...)` inside a `httpx.NewServer(...)` argument
// list).
func callHasOption(call *ast.CallExpr, optName string) bool {
	for _, arg := range call.Args {
		switch a := arg.(type) {
		case *ast.CallExpr:
			if sel, ok := a.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == optName {
				return true
			}
			if id, ok := a.Fun.(*ast.Ident); ok && id.Name == optName {
				return true
			}
		}
	}
	return false
}
