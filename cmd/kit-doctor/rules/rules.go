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
//
// Fix is optional. When non-nil and -interactive is set, kit-doctor
// prompts the operator to apply the fix. The contract for Fix:
//   - Must be idempotent (running twice yields the same end state).
//   - Must NOT delete files or perform destructive operations.
//   - On success, must return a single human-readable line describing
//     what changed (e.g. "appended docs/audit/dependency-allowlist.txt:
//     example.com/pkg # needs review (kit-doctor wave 153)").
//   - On failure, must return an error explaining the cause; partial
//     state changes must be rolled back where feasible.
//
// JSON output ignores Fix so the non-interactive contract is
// byte-for-byte unchanged.
type Finding struct {
	Rule       string
	Severity   Severity
	File       string
	Line       int
	Message    string
	Suggestion string
	Fix        func() (summary string, err error) `json:"-"`
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
		rateLimitOmissionRule{},
		websocketAnyOriginUnsafeRule{},
		websocketMissingMaxConnectionsRule{},
		centrifugeMissingJWTAuthRule{},
	}
}
