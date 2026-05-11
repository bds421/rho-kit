package rules

import (
	"go/ast"
	"go/token"
	"strconv"
	"strings"
)

// tenantKeyPrefixRule flags hand-written tenant Redis/cache key prefixes such
// as "tenant:" + id or fmt.Sprintf("tenant:%s:%s", id, key). Those bypass the
// length-prefixed core/tenant.Key encoder and can reintroduce cross-tenant key
// collision bugs.
type tenantKeyPrefixRule struct{}

func (tenantKeyPrefixRule) Name() string { return "tenant-key-prefix" }

func (r tenantKeyPrefixRule) Run(fset *token.FileSet, file *ast.File) []Finding {
	if file == nil {
		return nil
	}
	if strings.HasSuffix(fset.Position(file.Pos()).Filename, "_test.go") {
		return nil
	}

	var findings []Finding
	ast.Inspect(file, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		raw, err := strconv.Unquote(lit.Value)
		if err != nil || !looksLikeTenantKeyPrefix(raw) {
			return true
		}
		pos := fset.Position(lit.Pos())
		if isExempt(fset, file, r.Name(), pos.Filename, pos.Line) {
			return true
		}
		findings = append(findings, Finding{
			Rule:       r.Name(),
			Severity:   Warning,
			File:       pos.Filename,
			Line:       pos.Line,
			Message:    "hand-written tenant key prefix bypasses the canonical length-prefixed encoder",
			Suggestion: "use core/tenant.Key(ctx, parts...) or core/tenant.KeyFor(id, parts...) instead of building keys with a literal tenant: prefix",
		})
		return true
	})
	return findings
}

func looksLikeTenantKeyPrefix(s string) bool {
	if s == "tenant:" {
		return true
	}
	if !strings.HasPrefix(s, "tenant:") {
		return false
	}
	// Human-facing error strings in this repo use "tenant: <message>". The
	// risky key forms are compact prefixes such as "tenant:%s:" or
	// "tenant:" + tenantID, so keep the rule focused to avoid noisy findings.
	return len(s) > len("tenant:") && s[len("tenant:")] != ' '
}
