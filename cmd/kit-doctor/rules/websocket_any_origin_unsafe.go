package rules

import (
	"go/ast"
	"go/token"
	"strings"
)

// websocketAnyOriginUnsafeRule flags any call to
// `websocket.WithAnyOriginUnsafe()` outside test code. The option's
// name carries the warning, but coding agents copying from examples
// or migrating from a permissive setup may include it without
// realising it disables the kit's Origin check entirely — a
// websocket connection from any origin can then authenticate against
// the service's session cookies (cross-site WebSocket hijacking).
//
// The kit's safer alternatives are:
//
//   - `websocket.WithAllowedOrigins([]string{...})` — explicit allowlist
//   - Default (no option) — same-origin only, the strict policy
//
// Severity is HIGH (not CRITICAL): the function exists for legitimate
// use cases (browser extensions, internal tooling on a closed
// network) and the suppression marker is the documented escape hatch.
type websocketAnyOriginUnsafeRule struct{}

func (websocketAnyOriginUnsafeRule) Name() string { return "websocket-any-origin-unsafe" }

var websocketImports = []string{
	// Real module path consumers import; httpx/websocket has its own
	// go.mod (module github.com/bds421/rho-kit/httpx/websocket/v2).
	"github.com/bds421/rho-kit/httpx/websocket/v2",
	// Legacy transposed path kept for backwards compatibility with
	// existing callers/fixtures that referenced it.
	"github.com/bds421/rho-kit/httpx/v2/websocket",
}

func (r websocketAnyOriginUnsafeRule) Run(fset *token.FileSet, file *ast.File) []Finding {
	if file == nil {
		return nil
	}
	if strings.HasSuffix(fset.Position(file.Pos()).Filename, "_test.go") {
		return nil
	}
	aliases := map[string]struct{}{}
	for _, imp := range websocketImports {
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
		if !isPackageAliasCall(call, aliases, "WithAnyOriginUnsafe") {
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
			Message:    "websocket.WithAnyOriginUnsafe disables the same-origin check (cross-site WebSocket hijacking risk)",
			Suggestion: "prefer websocket.WithAllowedOrigins([]string{...}) with an explicit allowlist, or omit the option entirely for the default same-origin policy. Suppress with `// kit-doctor:allow websocket-any-origin-unsafe` only after documenting the closed-network or non-cookie-auth context that makes this safe.",
		})
		return true
	})
	return findings
}
