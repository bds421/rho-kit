package rules

import (
	"go/ast"
	"go/token"
)

// httpServerMissingErrorLogRule flags `httpx.NewServer(...)` calls
// that don't set `WithErrorLog`. Without it, the http.Server emits
// errors to the standard `log` package — which bypasses slog
// formatting and trace-ID correlation.
type httpServerMissingErrorLogRule struct{}

func (httpServerMissingErrorLogRule) Name() string { return "http-server-error-log" }

func (r httpServerMissingErrorLogRule) Run(fset *token.FileSet, file *ast.File) []Finding {
	var findings []Finding
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if !isPackageCall(call, "httpx", "NewServer") {
			return true
		}
		// httpx.NewServer takes opts variadically; check the call's
		// argument list directly (no fluent chain to walk).
		if !callHasOption(call, "WithErrorLog") {
			pos := fset.Position(call.Pos())
			findings = append(findings, Finding{
				Rule:       r.Name(),
				Severity:   Warning,
				File:       pos.Filename,
				Line:       pos.Line,
				Message:    "httpx.NewServer without WithErrorLog (server errors bypass slog)",
				Suggestion: "pass httpx.WithErrorLog(slogAdapter) so connection errors land in the structured log",
			})
		}
		return true
	})
	return findings
}
