package rules

import (
	"go/ast"
	"go/token"
)

// idempotencyMissingUserExtractorRule flags
// `idempotency.Middleware(...)` calls that lack `WithUserExtractor`
// AND `WithAllowSharedKeys` — without one of those, a single
// idempotency key collapses every caller into the same scope and
// allows replay across users.
type idempotencyMissingUserExtractorRule struct{}

func (idempotencyMissingUserExtractorRule) Name() string { return "idempotency-user-extractor" }

func (r idempotencyMissingUserExtractorRule) Run(fset *token.FileSet, file *ast.File) []Finding {
	var findings []Finding
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if !isPackageCall(call, "idempotency", "Middleware") {
			return true
		}
		if !chainHas(call, "WithUserExtractor", "WithAllowSharedKeys") {
			pos := fset.Position(call.Pos())
			findings = append(findings, Finding{
				Rule:       r.Name(),
				Severity:   Critical,
				File:       pos.Filename,
				Line:       pos.Line,
				Message:    "idempotency.Middleware without WithUserExtractor (cross-user replay risk)",
				Suggestion: "chain .WithUserExtractor(...) or, for explicitly shared scope, .WithAllowSharedKeys()",
			})
		}
		return true
	})
	return findings
}
