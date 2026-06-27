package rules

import (
	"go/ast"
	"go/token"
	"strings"
)

// idempotencyMissingUserExtractorRule flags
// `idempotency.Middleware(...)` calls that lack `WithUserExtractor`
// AND `WithAllowSharedKeys` — without one of those, a single
// idempotency key collapses every caller into the same scope and
// allows replay across users.
type idempotencyMissingUserExtractorRule struct{}

func (idempotencyMissingUserExtractorRule) Name() string { return "idempotency-user-extractor" }

func (r idempotencyMissingUserExtractorRule) Run(fset *token.FileSet, file *ast.File) []Finding {
	if file == nil {
		return nil
	}
	if strings.HasSuffix(fset.Position(file.Pos()).Filename, "_test.go") {
		return nil
	}
	idempotencyAliases := importAliasesFor(file, "github.com/bds421/rho-kit/httpx/v2/middleware/idempotency")
	if len(idempotencyAliases) == 0 {
		return nil
	}
	var findings []Finding
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if !isPackageAliasCall(call, idempotencyAliases, "Middleware") {
			return true
		}
		// Options spread from a slice (Middleware(store, opts...)) cannot
		// be inspected statically — stay silent rather than emit a
		// Critical false positive.
		if callOptionsUnverifiable(call) {
			return true
		}
		// WithUserExtractor / WithAllowSharedKeys are variadic options
		// passed as arguments to Middleware, not methods on a builder.
		if callHasOption(call, "WithUserExtractor") || callHasOption(call, "WithAllowSharedKeys") {
			return true
		}
		pos := fset.Position(call.Pos())
		if isExempt(fset, file, r.Name(), pos.Filename, pos.Line) {
			return true
		}
		findings = append(findings, Finding{
			Rule:       r.Name(),
			Severity:   Critical,
			File:       pos.Filename,
			Line:       pos.Line,
			Message:    "idempotency.Middleware without WithUserExtractor (cross-user replay risk)",
			Suggestion: "pass idempotency.WithUserExtractor(fn) or, for explicitly shared scope, idempotency.WithAllowSharedKeys() as an option; use idempotency.WithOptionalKey() when idempotency is opt-in (header present only)",
		})
		return true
	})
	return findings
}
