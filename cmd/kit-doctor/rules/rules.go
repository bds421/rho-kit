// Package rules holds the kit-doctor rule definitions and the
// shared types for findings, severity, and the per-file scan.
//
// A rule is a small static analyser that walks Go source and emits
// findings. Each rule lives in its own file so contributors can add
// one by writing a single Run function and registering it in
// [Registered].
package rules

import (
	"go/ast"
	"go/token"
)

// Severity ranks a finding so the CLI can decide an exit code.
type Severity int

const (
	// Info is purely advisory.
	Info Severity = iota
	// Warning suggests a tightening; ignored by `--strict=high`.
	Warning
	// High is an actual misconfiguration; default `--strict` floor.
	High
	// Critical is a security-impacting bug; default exit-1 floor.
	Critical
)

// String returns the severity's display label.
func (s Severity) String() string {
	switch s {
	case Critical:
		return "CRITICAL"
	case High:
		return "HIGH"
	case Warning:
		return "WARNING"
	case Info:
		return "INFO"
	}
	return "UNKNOWN"
}

// Finding is a single rule emission.
type Finding struct {
	Rule       string
	Severity   Severity
	File       string
	Line       int
	Message    string
	Suggestion string
}

// Rule defines one check. Run is invoked once per file in the target.
type Rule interface {
	// Name returns a stable identifier used in --enable / --disable
	// flags and CI output.
	Name() string
	// Run scans file and returns findings.
	Run(fset *token.FileSet, file *ast.File) []Finding
}

// Registered returns the default rule set in run order. Tests pass
// in their own slice to isolate behaviour.
func Registered() []Rule {
	return []Rule{
		jwtMissingClaimsRule{},
		idempotencyMissingUserExtractorRule{},
		idempotencyMemoryStoreRule{},
		tenantKeyPrefixRule{},
		defaultHTTPClientRule{},
		httpServerMissingErrorLogRule{},
		httpServerDirectConstructionRule{},
	}
}
