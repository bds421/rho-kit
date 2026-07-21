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
	if len(aliases) == 0 && !dotImport {
		return "", fmt.Errorf("no auth import in %s", path)
	}

	var fixed bool
	ast.Inspect(file, func(n ast.Node) bool {
		lit, ok := n.(*ast.CompositeLit)
		if !ok {
			return true
		}
		if fset.Position(lit.Pos()).Line != line {
			return true
		}
		if !isAuthIdentityType(lit.Type, aliases, dotImport) {
			return true
		}
		hasUserID, hasSubject, hasActor := identityLiteralFields(lit)
		if !hasUserID || hasSubject || hasActor {
			return true
		}
		var userIDExpr ast.Expr
		for _, elt := range lit.Elts {
			kv, ok := elt.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			key, ok := kv.Key.(*ast.Ident)
			if ok && key.Name == "UserID" {
				userIDExpr = kv.Value
				break
			}
		}
		if userIDExpr == nil {
			return true
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
		fixed = true
		return false
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