package rules

import (
	"go/ast"
	"go/token"
	"strings"
)

// websocketMissingMaxConnectionsRule flags any call to
// `websocket.Handle(...)` that does not include
// `WithMaxConnections(...)`. Without the cap, a single misbehaving
// client (or a small fleet) can hold open an unbounded number of
// websocket connections, each costing a goroutine and a file
// descriptor. The kit ships the option specifically to defeat the
// trivial DoS shape; coding agents wiring the handler from
// examples often skip optional parameters.
//
// Severity is WARNING: the omission is a saturation risk rather
// than an immediate confidentiality or integrity bug, and operators
// running behind an upstream rate-limiter (envoy, nginx) may
// reasonably rely on that for the cap. The suppression marker is
// the documented escape hatch.
type websocketMissingMaxConnectionsRule struct{}

func (websocketMissingMaxConnectionsRule) Name() string {
	return "websocket-missing-max-connections"
}

func (r websocketMissingMaxConnectionsRule) Run(fset *token.FileSet, file *ast.File) []Finding {
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
		if !isPackageAliasCall(call, aliases, "Handle") {
			return true
		}
		if callHasOption(call, "WithMaxConnections") {
			return true
		}
		pos := fset.Position(call.Pos())
		if isExempt(fset, file, r.Name(), pos.Filename, pos.Line) {
			return true
		}
		findings = append(findings, Finding{
			Rule:       r.Name(),
			Severity:   Warning,
			File:       pos.Filename,
			Line:       pos.Line,
			Message:    "websocket.Handle without WithMaxConnections (no server-side connection cap; goroutine / fd DoS risk)",
			Suggestion: "pass websocket.WithMaxConnections(n) with a value matched to your goroutine / fd budget. Suppress with `// kit-doctor:allow websocket-missing-max-connections` only when an upstream control (envoy, nginx, load balancer) already enforces the cap.",
		})
		return true
	})
	return findings
}
