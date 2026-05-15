package rules

import (
	"go/ast"
	"go/token"
	"strings"
)

// jwtMissingClaimsRule flags `jwt.Module(jwksURL, opts...)` calls that
// don't include both an issuer option (`WithIssuer` / `WithoutIssuer`)
// AND an audience option (`WithAudience` / `WithoutAudience`). Without
// an issuer or audience pin, federated services accept any signed
// token — defeating the protection JWT/PASETO offers.
//
// The runtime gate is fail-closed (app/jwt.Module panics at
// construction when either pair is missing), so this rule exists to
// surface the wiring bug pre-build in editor / CI.
type jwtMissingClaimsRule struct{}

func (jwtMissingClaimsRule) Name() string { return "jwt-missing-claims" }

// jwtModuleImports lists the import paths that expose the JWT Module
// constructor. Aliased re-exports inside the kit would surface as new
// entries here.
var jwtModuleImports = []string{
	"github.com/bds421/rho-kit/app/jwt/v2",
}

func (r jwtMissingClaimsRule) Run(fset *token.FileSet, file *ast.File) []Finding {
	if file == nil {
		return nil
	}
	if strings.HasSuffix(fset.Position(file.Pos()).Filename, "_test.go") {
		return nil
	}
	aliases := map[string]struct{}{}
	for _, imp := range jwtModuleImports {
		for name := range importAliasesFor(file, imp) {
			aliases[name] = struct{}{}
		}
	}
	if len(aliases) == 0 {
		return nil
	}
	var findings []Finding
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if !isPackageAliasCall(call, aliases, "Module") {
			return true
		}
		hasIssuer := callHasOption(call, "WithIssuer") || callHasOption(call, "WithoutIssuer")
		hasAud := callHasOption(call, "WithAudience") || callHasOption(call, "WithoutAudience")

		pos := fset.Position(call.Pos())
		if isExempt(fset, file, r.Name(), pos.Filename, pos.Line) {
			return true
		}
		if !hasIssuer {
			findings = append(findings, Finding{
				Rule:       r.Name(),
				Severity:   Critical,
				File:       pos.Filename,
				Line:       pos.Line,
				Message:    "jwt.Module called without an issuer pin",
				Suggestion: "pass jwt.WithIssuer(\"https://issuer.example.com\") or explicit jwt.WithoutIssuer()",
			})
		}
		if !hasAud {
			findings = append(findings, Finding{
				Rule:       r.Name(),
				Severity:   High,
				File:       pos.Filename,
				Line:       pos.Line,
				Message:    "jwt.Module called without an audience pin",
				Suggestion: "pass jwt.WithAudience(\"my-service\") or explicit jwt.WithoutAudience()",
			})
		}
		return true
	})
	return findings
}
