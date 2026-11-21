package rules

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/printer"
	"go/token"
	"os"
)

func fixAuthIdentityDrift(path string, line int) (string, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, src, parser.ParseComments)
	if err != nil {
		return "", fmt.Errorf("parse %s: %w", path, err)
	}
	aliases, dotImport := authImportNames(file)
	if len(aliases) == 0 && !dotImport {
		return "", fmt.Errorf("no auth import in %s", path)
	}

	var fixed bool
	ast.Inspect(file, func(n ast.Node) bool {
		lit, ok := n.(*ast.CompositeLit)
		if !ok || fset.Position(lit.Pos()).Line != line {
			return true
		}
		if addActorFields(lit, aliases, dotImport) {
			fixed = true
			return false
		}
		return true
	})
	if !fixed {
		return "", fmt.Errorf("identity literal at %s:%d not fixable", path, line)
	}

	var buf bytes.Buffer
	if err := printer.Fprint(&buf, fset, file); err != nil {
		return "", err
	}
	formatted, err := format.Source(buf.Bytes())
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, formatted, 0o644); err != nil {
		return "", err
	}
	return fmt.Sprintf("added Subject, Actor, and ActorKind to auth.Identity at %s:%d", path, line), nil
}

// authImportNames returns the names file imports the auth middleware
// packages under, and whether it dot-imports one of them.
func authImportNames(file *ast.File) (map[string]struct{}, bool) {
	aliases := map[string]struct{}{}
	dotImport := false
	for _, imp := range authMiddlewareImports {
		for name := range importAliasesFor(file, imp) {
			aliases[name] = struct{}{}
		}
		if hasDotImport(file, imp) {
			dotImport = true
		}
	}
	return aliases, dotImport
}

// addActorFields prepends Subject, Actor and ActorKind, taken from UserID,
// to lit when it is an auth.Identity literal that sets UserID but neither
// Subject nor Actor. It reports whether it changed lit.
func addActorFields(lit *ast.CompositeLit, aliases map[string]struct{}, dotImport bool) bool {
	if !isAuthIdentityType(lit.Type, aliases, dotImport) {
		return false
	}
	hasUserID, hasSubject, hasActor := identityLiteralFields(lit)
	if !hasUserID || hasSubject || hasActor {
		return false
	}
	userIDExpr := keyedValue(lit, "UserID")
	if userIDExpr == nil {
		return false
	}
	authAlias := authPackageAlias(lit.Type, aliases)
	lit.Elts = append([]ast.Expr{
		&ast.KeyValueExpr{Key: &ast.Ident{Name: "Subject"}, Value: userIDExpr},
		&ast.KeyValueExpr{Key: &ast.Ident{Name: "Actor"}, Value: userIDExpr},
		&ast.KeyValueExpr{
			Key: &ast.Ident{Name: "ActorKind"},
			Value: &ast.SelectorExpr{
				X:   &ast.Ident{Name: authAlias},
				Sel: &ast.Ident{Name: "ActorUser"},
			},
		},
	}, lit.Elts...)
	return true
}

// keyedValue is the value lit gives field, or nil.
func keyedValue(lit *ast.CompositeLit, field string) ast.Expr {
	for _, elt := range lit.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		key, ok := kv.Key.(*ast.Ident)
		if ok && key.Name == field {
			return kv.Value
		}
	}
	return nil
}

func authPackageAlias(typeExpr ast.Expr, aliases map[string]struct{}) string {
	switch t := typeExpr.(type) {
	case *ast.Ident:
		if _, ok := aliases[t.Name]; ok {
			return t.Name
		}
	case *ast.SelectorExpr:
		if ident, ok := t.X.(*ast.Ident); ok {
			if _, ok := aliases[ident.Name]; ok {
				return ident.Name
			}
		}
	}
	for name := range aliases {
		return name
	}
	return "auth"
}
