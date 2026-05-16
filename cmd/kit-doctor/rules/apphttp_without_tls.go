package rules

import (
	"go/ast"
	"go/token"
	"strings"
)

// apphttpWithoutTLSRule flags any call to `apphttp.WithoutTLS()`
// in non-test code. The option exists for the kit's reference
// examples (and the rare service running behind a TLS-terminating
// proxy whose operator has audited the path), but it should be a
// deliberate, suppressed choice in production code — not a
// pattern coding agents copy from the api-gateway example.
//
// The kit's always-on TLS validator (FR-014) is the canonical
// guardrail; this option turns it off, so callers MUST either:
//
//   - acknowledge with `// kit-doctor:allow apphttp-without-tls`
//     on (or directly above) the offending line, OR
//   - replace the call with a proper TLS configuration via
//     `app.BaseConfig.TLS`.
//
// Severity is HIGH (not CRITICAL): the option exists for
// legitimate reasons (mesh-terminated TLS, dev-only services),
// but uncritical use turns the validator's protection off.
type apphttpWithoutTLSRule struct{}

func (apphttpWithoutTLSRule) Name() string { return "apphttp-without-tls" }

var apphttpImports = []string{
	"github.com/bds421/rho-kit/app/http/v2",
}

func (r apphttpWithoutTLSRule) Run(fset *token.FileSet, file *ast.File) []Finding {
	if file == nil {
		return nil
	}
	filename := fset.Position(file.Pos()).Filename
	if strings.HasSuffix(filename, "_test.go") {
		return nil
	}
	aliases := map[string]struct{}{}
	for _, imp := range apphttpImports {
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
		if !isPackageAliasCall(call, aliases, "WithoutTLS") {
			return true
		}
		pos := fset.Position(call.Pos())
		if isExempt(fset, file, r.Name(), pos.Filename, pos.Line) {
			return true
		}
		findings = append(findings, Finding{
			Rule:       r.Name(),
			Severity:   High,
			File:       pos.Filename,
			Line:       pos.Line,
			Message:    "apphttp.WithoutTLS disables the kit's always-on TLS validator",
			Suggestion: "configure TLS via app.BaseConfig.TLS; or suppress with `// kit-doctor:allow apphttp-without-tls` only after documenting why (TLS-terminating proxy, dev-only service, etc.)",
		})
		return true
	})
	return findings
}
