package rules

import (
	"go/ast"
	"go/token"
	"strings"
)

// authIdentityActorRule flags [auth.Identity] composite literals that set the
// deprecated UserID field without Actor or Subject, which drops machine
// attribution at the middleware boundary.
type authIdentityActorRule struct{}

func (authIdentityActorRule) Name() string { return "auth-identity-actor-drift" }

var authMiddlewareImports = []string{
	"github.com/bds421/rho-kit/httpx/v2/middleware/auth",
}

func (r authIdentityActorRule) Run(fset *token.FileSet, file *ast.File) []Finding {
	if file == nil {
		return nil
	}
	if strings.HasSuffix(fset.Position(file.Pos()).Filename, "_test.go") {
		return nil
	}
	aliases := map[string]struct{}{}
	for _, imp := range authMiddlewareImports {
		for name := range importAliasesFor(file, imp) {
			aliases[name] = struct{}{}
		}
	}
	if len(aliases) == 0 {
		return nil
	}
	var findings []Finding
	ast.Inspect(file, func(n ast.Node) bool {
		lit, ok := n.(*ast.CompositeLit)
		if !ok {
			return true
		}
		if !isAuthIdentityType(lit.Type, aliases) {
			return true
		}
		hasUserID, hasSubject, hasActor := identityLiteralFields(lit)
		if !hasUserID || hasSubject || hasActor {
			return true
		}
		pos := fset.Position(lit.Pos())
		fixPath := pos.Filename
		fixLine := pos.Line
		findings = append(findings, Finding{
			Rule:       r.Name(),
			Severity:   Warning,
			File:       pos.Filename,
			Line:       pos.Line,
			Message:    "auth.Identity sets UserID without Actor or Subject — machine attribution is lost after the identity split",
			Suggestion: "set Subject and Actor (and ActorKind for machine credentials), or use auth.IdentityFromScopedKey for scoped API keys",
			Fix: func() (string, error) {
				return fixAuthIdentityDrift(fixPath, fixLine)
			},
		})
		return true
	})
	return findings
}

func isAuthIdentityType(expr ast.Expr, aliases map[string]struct{}) bool {
	switch t := expr.(type) {
	case *ast.Ident:
		return t.Name == "Identity"
	case *ast.SelectorExpr:
		if t.Sel.Name != "Identity" {
			return false
		}
		ident, ok := t.X.(*ast.Ident)
		if !ok {
			return false
		}
		_, ok = aliases[ident.Name]
		return ok
	default:
		return false
	}
}

func identityLiteralFields(lit *ast.CompositeLit) (hasUserID, hasSubject, hasActor bool) {
	for _, elt := range lit.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		key, ok := kv.Key.(*ast.Ident)
		if !ok {
			continue
		}
		switch key.Name {
		case "UserID":
			hasUserID = true
		case "Subject":
			hasSubject = true
		case "Actor":
			hasActor = true
		}
	}
	return hasUserID, hasSubject, hasActor
}