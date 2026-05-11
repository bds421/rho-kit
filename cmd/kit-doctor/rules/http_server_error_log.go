package rules

import (
	"go/ast"
	"go/token"
	"strings"
)

// httpServerMissingErrorLogRule flags `httpx.NewServer(...)` calls
// that don't set `WithErrorLog`. NewServer has a safe slog-backed
// fallback, but direct manual wiring should still pass the service
// logger so connection-level errors use the same handler and fields as
// the rest of the service logs.
type httpServerMissingErrorLogRule struct{}

func (httpServerMissingErrorLogRule) Name() string { return "http-server-error-log" }

func (r httpServerMissingErrorLogRule) Run(fset *token.FileSet, file *ast.File) []Finding {
	if file == nil {
		return nil
	}
	if strings.HasSuffix(fset.Position(file.Pos()).Filename, "_test.go") {
		return nil
	}
	httpxAliases := importAliasesFor(file, "github.com/bds421/rho-kit/httpx/v2")
	if len(httpxAliases) == 0 {
		return nil
	}
	var findings []Finding
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if !isPackageAliasCall(call, httpxAliases, "NewServer") {
			return true
		}
		// httpx.NewServer takes opts variadically; check the call's
		// argument list directly (no fluent chain to walk).
		if !callHasOption(call, "WithErrorLog") {
			pos := fset.Position(call.Pos())
			if isExempt(fset, file, r.Name(), pos.Filename, pos.Line) {
				return true
			}
			findings = append(findings, Finding{
				Rule:       r.Name(),
				Severity:   Warning,
				File:       pos.Filename,
				Line:       pos.Line,
				Message:    "httpx.NewServer without WithErrorLog (connection errors use slog.Default instead of the service logger)",
				Suggestion: "pass httpx.WithErrorLog(slog.NewLogLogger(logger.Handler(), slog.LevelWarn)) when manually wiring servers",
			})
		}
		return true
	})
	return findings
}
