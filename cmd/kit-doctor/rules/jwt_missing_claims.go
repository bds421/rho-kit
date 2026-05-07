package rules

import (
	"go/ast"
	"go/token"
)

// jwtMissingClaimsRule flags `WithJWT` calls that don't chain
// `WithExpectedIssuer` AND `WithExpectedAudience`. Without an issuer
// or audience pin, federated services accept any signed token —
// defeating the protection JWT/PASETO offers.
type jwtMissingClaimsRule struct{}

func (jwtMissingClaimsRule) Name() string { return "jwt-missing-claims" }

func (r jwtMissingClaimsRule) Run(fset *token.FileSet, file *ast.File) []Finding {
	var findings []Finding
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if !isMethodCall(call, "WithJWT") {
			return true
		}
		hasIssuer := chainHas(call, "WithExpectedIssuer", "WithJWTIssuer", "WithoutJWTIssuer", "WithAllowAnyIssuer")
		hasAud := chainHas(call, "WithExpectedAudience", "WithJWTAudience", "WithoutJWTAudience", "WithAllowAnyAudience")

		pos := fset.Position(call.Pos())
		if !hasIssuer {
			findings = append(findings, Finding{
				Rule:       r.Name(),
				Severity:   Critical,
				File:       pos.Filename,
				Line:       pos.Line,
				Message:    "WithJWT called without an issuer pin",
				Suggestion: "chain .WithJWTIssuer(\"https://issuer.example.com\") or explicit .WithoutJWTIssuer()",
			})
		}
		if !hasAud {
			findings = append(findings, Finding{
				Rule:       r.Name(),
				Severity:   High,
				File:       pos.Filename,
				Line:       pos.Line,
				Message:    "WithJWT called without an audience pin",
				Suggestion: "chain .WithJWTAudience(\"my-service\") or explicit .WithoutJWTAudience()",
			})
		}
		return true
	})
	return findings
}
