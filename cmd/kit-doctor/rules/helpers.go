package rules

import (
	"go/ast"
	"path"
	"strconv"
	"sync"
)

// importAliasesFor returns the set of identifiers that resolve to the
// given import path inside file. Handles named aliases, dot imports
// (which contribute no alias), blank imports (skipped), and the
// default last-path-segment alias.
func importAliasesFor(file *ast.File, importPath string) map[string]struct{} {
	out := make(map[string]struct{})
	if file == nil {
		return out
	}
	for _, imp := range file.Imports {
		raw, err := strconv.Unquote(imp.Path.Value)
		if err != nil || raw != importPath {
			continue
		}
		if imp.Name == nil {
			out[defaultImportName(raw)] = struct{}{}
			continue
		}
		switch imp.Name.Name {
		case "_", ".":
			continue
		default:
			out[imp.Name.Name] = struct{}{}
		}
	}
	return out
}

func defaultImportName(importPath string) string {
	name := path.Base(importPath)
	if isSemanticImportVersion(name) {
		parent := path.Base(path.Dir(importPath))
		if parent != "." && parent != "/" {
			return parent
		}
	}
	return name
}

func isSemanticImportVersion(name string) bool {
	if len(name) < 2 || name[0] != 'v' {
		return false
	}
	for _, r := range name[1:] {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func isMethodCall(call *ast.CallExpr, name string) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	return sel.Sel.Name == name
}

func isPackageAliasCall(call *ast.CallExpr, aliases map[string]struct{}, fn string) bool {
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
	if _, ok := aliases[ident.Name]; !ok {
		return false
	}
	return ident.Obj == nil || ident.Obj.Kind == ast.Pkg
}

func isPackageAlias(ident *ast.Ident, aliases map[string]struct{}) bool {
	if ident == nil {
		return false
	}
	if _, ok := aliases[ident.Name]; !ok {
		return false
	}
	return ident.Obj == nil || ident.Obj.Kind == ast.Pkg
}

// chainHas reports whether any of the named methods appears as a
// selector in the same fluent call chain as call. The chain is
// resolved by walking up parent links until the call is no longer
// the receiver of an outer method call, then walking back down
// through every nested CallExpr-on-SelectorExpr the resulting root
// dominates. Sibling Builders elsewhere in the file are ignored.
func chainHas(call *ast.CallExpr, names ...string) bool {
	if call == nil {
		return false
	}
	want := make(map[string]struct{}, len(names))
	for _, n := range names {
		want[n] = struct{}{}
	}

	root := chainTop(call)
	found := false
	visitChain(root, func(sel string) bool {
		if _, ok := want[sel]; ok {
			found = true
			return false
		}
		return true
	})
	return found
}

// chainTop walks parent pointers upward while call is the X of an
// enclosing SelectorExpr that is the Fun of an enclosing CallExpr
// (i.e. while call is the receiver of an outer fluent method call),
// returning the outermost CallExpr in the chain.
func chainTop(call *ast.CallExpr) *ast.CallExpr {
	cur := ast.Node(call)
	for {
		parent, ok := parentOf(cur)
		if !ok {
			return cur.(*ast.CallExpr)
		}
		sel, ok := parent.(*ast.SelectorExpr)
		if !ok || sel.X != cur {
			return cur.(*ast.CallExpr)
		}
		grand, ok := parentOf(sel)
		if !ok {
			return cur.(*ast.CallExpr)
		}
		gc, ok := grand.(*ast.CallExpr)
		if !ok || gc.Fun != sel {
			return cur.(*ast.CallExpr)
		}
		cur = gc
	}
}

// visitChain walks every selector name along the fluent chain rooted
// at call, descending through nested CallExpr.Fun.SelectorExpr.X
// links. Visit returns false to stop early.
func visitChain(call *ast.CallExpr, visit func(string) bool) {
	cur := ast.Expr(call)
	for {
		c, ok := cur.(*ast.CallExpr)
		if !ok {
			return
		}
		sel, ok := c.Fun.(*ast.SelectorExpr)
		if !ok {
			return
		}
		if !visit(sel.Sel.Name) {
			return
		}
		cur = sel.X
	}
}

// parents back the chain-walking helpers above; SetCurrentFile rebuilds
// the map for each scanned file. Guarded so a future parallel scan cannot
// race concurrent map access (review-24).
var (
	parentsMu sync.RWMutex
	parents   map[ast.Node]ast.Node
)

func SetCurrentFile(f *ast.File) {
	m := buildParents(f)
	parentsMu.Lock()
	parents = m
	parentsMu.Unlock()
}

func parentOf(n ast.Node) (ast.Node, bool) {
	parentsMu.RLock()
	defer parentsMu.RUnlock()
	if parents == nil {
		return nil, false
	}
	p, ok := parents[n]
	return p, ok
}

func buildParents(f *ast.File) map[ast.Node]ast.Node {
	if f == nil {
		return nil
	}
	out := make(map[ast.Node]ast.Node)
	ast.Walk(parentVisitor{parent: nil, m: out}, f)
	return out
}

type parentVisitor struct {
	parent ast.Node
	m      map[ast.Node]ast.Node
}

func (v parentVisitor) Visit(n ast.Node) ast.Visitor {
	if n == nil {
		return nil
	}
	if v.parent != nil {
		v.m[n] = v.parent
	}
	return parentVisitor{parent: n, m: v.m}
}

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

// callOptionsUnverifiable reports whether the option arguments to call
// cannot be inspected statically, so an option-presence rule must not
// fire on it. True when:
//
//   - the call spreads a slice (`Module(url, opts...)`), or
//   - any trailing argument is a non-call expression (e.g. a scalar
//     variable `opt := WithUserExtractor(fn); Middleware(store, opt)`).
//
// callHasOption only recognises inline CallExpr options, so treating
// variables / other expressions as "definitely absent" would emit
// Critical false positives on correctly-configured code. Stay silent.
func callOptionsUnverifiable(call *ast.CallExpr) bool {
	if call == nil {
		return false
	}
	if call.Ellipsis.IsValid() {
		return true
	}
	// Skip args[0]: kit constructors almost always take a required
	// resource (store, handler, JWKS URL) as the first positional
	// argument. Treating that Ident as an "option we cannot verify"
	// would silence every Middleware(store) / NewServer(handler) finding.
	// Trailing non-call, non-literal args may be prebuilt option values
	// (opt := WithUserExtractor(fn); Middleware(store, opt)).
	for i, arg := range call.Args {
		if i == 0 {
			continue
		}
		switch arg.(type) {
		case *ast.CallExpr:
			continue
		case *ast.BasicLit:
			continue
		default:
			return true
		}
	}
	return false
}
