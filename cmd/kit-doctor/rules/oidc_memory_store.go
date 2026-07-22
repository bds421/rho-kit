package rules

import (
	"go/ast"
	"go/token"
	"strings"
)

// oidcMemoryStoreRule rejects the explicitly test-only OAuth browser stores in
// non-test source. Browser login needs state and session continuity across
// replicas; the memory stores lose both on a restart and cannot reject a
// callback replay delivered to another replica. auth/oauth2/redis is the
// provider-neutral durable baseline.
type oidcMemoryStoreRule struct{}

func (oidcMemoryStoreRule) Name() string { return "oidc-memory-store" }

func (r oidcMemoryStoreRule) Run(fset *token.FileSet, file *ast.File) []Finding {
	if file == nil || strings.HasSuffix(fset.Position(file.Pos()).Filename, "_test.go") {
		return nil
	}
	aliases := importAliasesFor(file, "github.com/bds421/rho-kit/auth/oauth2/v2")
	if len(aliases) == 0 {
		return nil
	}
	var findings []Finding
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || (!isPackageAliasCall(call, aliases, "NewMemorySessionStore") && !isPackageAliasCall(call, aliases, "NewMemoryStateStore")) {
			return true
		}
		pos := fset.Position(call.Pos())
		if isExempt(fset, file, r.Name(), pos.Filename, pos.Line) {
			return true
		}
		findings = append(findings, Finding{
			Rule: r.Name(), Severity: High, File: pos.Filename, Line: pos.Line,
			Message:    "auth/oauth2 in-memory browser store is not safe across restarts or replicas",
			Suggestion: "use auth/oauth2/redis.NewSessionStore and redis.NewStateStore; a reviewed single-process tool may suppress with `// kit-doctor:allow oidc-memory-store owner=... reason=... review=... posture=...`",
		})
		return true
	})
	return findings
}
