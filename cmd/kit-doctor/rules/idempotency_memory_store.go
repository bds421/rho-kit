package rules

import (
	"go/ast"
	"go/token"
	"strings"
)

// idempotencyMemoryStoreRule flags any call to
// `idempotency.NewMemoryStore` outside test code. AGENTS.md explicitly
// forbids the in-memory store in production: it has no cross-process
// sharing, so two replicas with the same idempotency key will both
// process the request, defeating the point of the middleware.
// THREAT_MODEL §4.9 I-05 reinforces the same constraint —
// "kit-doctor flags it" is part of the documented mitigation.
//
// The check is intentionally narrow:
//   - It matches the exported constructor `NewMemoryStore` reached
//     through the data-module idempotency package (or any alias).
//   - It skips `_test.go` so the test surface is unaffected.
//   - It honours the standard inline suppression marker
//     (`// kit-doctor:allow idempotency-memory-store`) for tooling that
//     deliberately wires a memory store in non-test entry points.
type idempotencyMemoryStoreRule struct{}

func (idempotencyMemoryStoreRule) Name() string { return "idempotency-memory-store" }

// idempotencyMemoryStoreImports lists the import paths that expose
// the offending constructor. The data module owns the canonical
// implementation; aliased re-exports inside the kit are unlikely but
// would surface as a new alias here if added.
var idempotencyMemoryStoreImports = []string{
	"github.com/bds421/rho-kit/data/v2/idempotency",
}

func (r idempotencyMemoryStoreRule) Run(fset *token.FileSet, file *ast.File) []Finding {
	if file == nil {
		return nil
	}
	if strings.HasSuffix(fset.Position(file.Pos()).Filename, "_test.go") {
		return nil
	}
	aliases := map[string]struct{}{}
	for _, imp := range idempotencyMemoryStoreImports {
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
		if !isPackageAliasCall(call, aliases, "NewMemoryStore") {
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
			Message:    "idempotency.NewMemoryStore is in-memory only and breaks idempotency across replicas",
			Suggestion: "use idempotency/redisstore.New or idempotency/pgstore.New for multi-instance deployments; suppress with `// kit-doctor:allow idempotency-memory-store` only for explicitly single-instance tooling",
		})
		return true
	})
	return findings
}
