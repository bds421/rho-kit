package main

import (
	"fmt"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"

	"github.com/bds421/rho-kit/cmd/kit-doctor/rules"
)

// scan walks root, parses every .go file (skipping vendor and
// generated suffixes), and returns the union of all rule findings.
func scan(root string, ruleSet []rules.Rule) ([]rules.Finding, error) {
	var findings []rules.Finding
	fset := token.NewFileSet()

	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "vendor" || d.Name() == "node_modules" || strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		// Skip generated files heuristically. A real implementation
		// would inspect the // Code generated header.
		if strings.HasSuffix(path, "_gen.go") || strings.HasSuffix(path, ".pb.go") {
			return nil
		}

		f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			// Don't fail the whole scan on one unparseable file; surface
			// it as a finding so the operator notices.
			findings = append(findings, rules.Finding{
				Rule:     "parse-error",
				Severity: rules.Warning,
				File:     path,
				Line:     0,
				Message:  fmt.Sprintf("parse failed: %v", err),
			})
			return nil
		}
		rules.SetCurrentFile(f)
		for _, r := range ruleSet {
			findings = append(findings, r.Run(fset, f)...)
		}
		return nil
	})
	if walkErr != nil {
		return nil, walkErr
	}

	sort.SliceStable(findings, func(i, j int) bool {
		if findings[i].File != findings[j].File {
			return findings[i].File < findings[j].File
		}
		return findings[i].Line < findings[j].Line
	})
	return findings, nil
}

// formatFindings renders the findings as the audit-style checklist.
func formatFindings(findings []rules.Finding) string {
	if len(findings) == 0 {
		return "✓ no findings\n"
	}
	var b strings.Builder
	for _, f := range findings {
		fmt.Fprintf(&b, "✗ %s [%s]: %s\n  at %s:%d\n",
			f.Severity, f.Rule, f.Message, f.File, f.Line)
		if f.Suggestion != "" {
			fmt.Fprintf(&b, "  fix: %s\n", f.Suggestion)
		}
	}
	return b.String()
}

// exitCode returns the kit-doctor exit code for findings:
//
//	0 — no findings at or above the floor.
//	1 — one or more findings at or above the floor.
//	2 — tool error (handled by the caller).
func exitCode(findings []rules.Finding, floor rules.Severity) int {
	for _, f := range findings {
		if f.Severity >= floor {
			return 1
		}
	}
	return 0
}
