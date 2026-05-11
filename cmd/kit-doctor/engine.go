package main

import (
	"bufio"
	"fmt"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/bds421/rho-kit/cmd/kit-doctor/v2/rules"
)

// generatedHeaderRE matches the canonical generated-code header
// described in https://golang.org/s/generatedcode. We also accept the
// missing-space variant some tools emit (`//Code generated`).
var generatedHeaderRE = regexp.MustCompile(`^//\s?Code generated .* DO NOT EDIT\.$`)

// scan walks root, parses every .go file (skipping vendor and
// generated files), and returns the union of all rule findings.
func scan(root string, ruleSet []rules.Rule) ([]rules.Finding, error) {
	var findings []rules.Finding
	fset := token.NewFileSet()

	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if filepath.Clean(path) == filepath.Clean(root) {
				return nil
			}
			if d.Name() == "vendor" || d.Name() == "node_modules" || strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			findings = append(findings, rules.Finding{
				Rule:     "symlinked-go-file",
				Severity: rules.Warning,
				File:     path,
				Line:     0,
				Message:  "symlinked Go file skipped to keep scan inside root",
			})
			return nil
		}
		gen, genErr := isGeneratedFile(path)
		if genErr != nil {
			findings = append(findings, rules.Finding{
				Rule:     "io-error",
				Severity: rules.Warning,
				File:     path,
				Line:     0,
				Message:  fmt.Sprintf("read failed: %v", genErr),
			})
			return nil
		}
		if gen {
			return nil
		}

		f, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
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

// isGeneratedFile reports whether path begins with the canonical
// `// Code generated ... DO NOT EDIT.` header within the first 20
// lines. Returns false if no header is found before the first non-
// comment, non-blank line.
func isGeneratedFile(path string) (gen bool, err error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer func() {
		if cerr := f.Close(); cerr != nil && err == nil {
			err = cerr
		}
	}()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for i := 0; i < 20 && scanner.Scan(); i++ {
		line := strings.TrimRight(scanner.Text(), "\r")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if !strings.HasPrefix(trimmed, "//") {
			return false, nil
		}
		if generatedHeaderRE.MatchString(line) {
			return true, nil
		}
	}
	return false, scanner.Err()
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
