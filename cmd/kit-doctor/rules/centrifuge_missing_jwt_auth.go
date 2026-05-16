package rules

import (
	"go/ast"
	"go/token"
	"strings"
)

// centrifugeMissingJWTAuthRule flags any call to
// `centrifuge.NewNode(...)` that does not include
// `WithJWTAuth(...)`. Without an auth provider, every realtime
// connection is accepted regardless of identity — analogous to the
// `idempotency-user-extractor` rule's scope-collapse risk but for
// realtime broadcasts: any client can subscribe to any channel and
// publish (when publishes are accepted).
//
// The exception case — deliberately open / public-broadcast realtime
// — should be acknowledged via the suppression marker so reviewers
// understand the wiring is intentional.
//
// Severity is CRITICAL: an unauthenticated realtime endpoint is a
// data-exfiltration channel for any in-process state the service
// broadcasts.
type centrifugeMissingJWTAuthRule struct{}

func (centrifugeMissingJWTAuthRule) Name() string { return "centrifuge-missing-jwt-auth" }

var centrifugeImports = []string{
	"github.com/bds421/rho-kit/realtime/v2/centrifuge",
}

func (r centrifugeMissingJWTAuthRule) Run(fset *token.FileSet, file *ast.File) []Finding {
	if file == nil {
		return nil
	}
	if strings.HasSuffix(fset.Position(file.Pos()).Filename, "_test.go") {
		return nil
	}
	aliases := map[string]struct{}{}
	for _, imp := range centrifugeImports {
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
		if !isPackageAliasCall(call, aliases, "NewNode") {
			return true
		}
		if callHasOption(call, "WithJWTAuth") {
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
			Message:    "centrifuge.NewNode without WithJWTAuth (unauthenticated realtime subscribers / publishers)",
			Suggestion: "pass centrifuge.WithJWTAuth(jwtProvider) to gate connections on a JWT verifier. Suppress with `// kit-doctor:allow centrifuge-missing-jwt-auth` only for deliberately public-broadcast realtime endpoints whose channel content is non-sensitive.",
		})
		return true
	})
	return findings
}
