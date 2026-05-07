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
		// WithUserExtractor / WithAllowSharedKeys are variadic options
		// passed as arguments to Middleware, not methods on a builder.
		if callHasOption(call, "WithUserExtractor") || callHasOption(call, "WithAllowSharedKeys") {
			return true
		}
		pos := fset.Position(call.Pos())
		findings = append(findings, Finding{
			Rule:       r.Name(),
			Severity:   Critical,
			File:       pos.Filename,
			Line:       pos.Line,
			Message:    "idempotency.Middleware without WithUserExtractor (cross-user replay risk)",
			Suggestion: "pass idempotency.WithUserExtractor(fn) or, for explicitly shared scope, idempotency.WithAllowSharedKeys() as an option",
		})
		return true
	})
	return findings
}
