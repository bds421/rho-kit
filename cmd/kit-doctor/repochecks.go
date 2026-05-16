// Repository-level checks emit findings with attached Fix functions
// that the -interactive mode can apply. Unlike the AST rules in
// ./rules, these inspect repository-shaped artefacts (CODEOWNERS,
// dependency allowlists, go.work.sum, env-var schemas) so the fixes
// can be safe, idempotent file edits.
//
// Safety contract for every Fix:
//   - idempotent: running twice yields the same end state;
//   - additive only: append a line or run `go work sync`; never delete;
//   - paper trail: returns a human-readable summary describing the
//     exact change so the operator can audit it.
package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/bds421/rho-kit/cmd/kit-doctor/v2/rules"
)

// repoChecker is one repo-level check. Each returns zero or more
// findings, some of which carry a Fix function.
type repoChecker func(root string) ([]rules.Finding, error)

// repoCheckers returns the default repo-check set. Tests pass their
// own slice to isolate behaviour.
func repoCheckers() []repoChecker {
	return []repoChecker{
		checkCodeownersSecuritySensitive,
		checkDependencyAllowlist,
		checkGoWorkSum,
		checkServiceConfigEnvVars,
	}
}

// runRepoCheckers invokes every repo-level checker against root and
// returns the union of findings. Errors from a single checker are
// surfaced as a Warning-severity finding so a single bad checker does
// not abort the whole run.
func runRepoCheckers(root string, checkers []repoChecker) []rules.Finding {
	var out []rules.Finding
	for _, c := range checkers {
		findings, err := c(root)
		if err != nil {
			out = append(out, rules.Finding{
				Rule:     "repo-check-error",
				Severity: rules.Warning,
				File:     root,
				Message:  fmt.Sprintf("repo check failed: %v", err),
			})
			continue
		}
		out = append(out, findings...)
	}
	return out
}

// securitySensitivePaths lists files whose changes warrant explicit
// CODEOWNERS review. Adding new entries here grows the checker.
var securitySensitivePaths = []string{
	"crypto/",
	"security/",
	"authz/",
	"SECURITY.md",
}

// checkCodeownersSecuritySensitive flags security-sensitive paths
// that are not covered by .github/CODEOWNERS. The fix appends a
// review-team owner line. Idempotent: the fix checks the file before
// writing.
func checkCodeownersSecuritySensitive(root string) ([]rules.Finding, error) {
	coPath := filepath.Join(root, ".github", "CODEOWNERS")
	contents, err := os.ReadFile(coPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read CODEOWNERS: %w", err)
	}
	have := codeownersPatterns(string(contents))
	var findings []rules.Finding
	for _, want := range securitySensitivePaths {
		if codeownersHasPattern(have, want) {
			continue
		}
		want := want // capture for closure
		findings = append(findings, rules.Finding{
			Rule:       "codeowners-missing-security-path",
			Severity:   rules.High,
			File:       coPath,
			Message:    fmt.Sprintf("security-sensitive path %q has no CODEOWNERS entry", want),
			Suggestion: fmt.Sprintf("append `%s @security-review` to %s", want, coPath),
			Fix: func() (string, error) {
				return appendCodeownersLine(coPath, want, "@security-review")
			},
		})
	}
	return findings, nil
}

// codeownersPatterns returns the set of patterns declared on
// non-comment lines of CODEOWNERS. Patterns are the first whitespace-
// separated field.
func codeownersPatterns(s string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, line := range strings.Split(s, "\n") {
		trim := strings.TrimSpace(line)
		if trim == "" || strings.HasPrefix(trim, "#") {
			continue
		}
		fields := strings.Fields(trim)
		if len(fields) == 0 {
			continue
		}
		out[fields[0]] = struct{}{}
	}
	return out
}

// codeownersHasPattern reports whether the existing CODEOWNERS
// pattern set already covers want. Exact match only; we don't try to
// reason about prefix overlaps.
func codeownersHasPattern(have map[string]struct{}, want string) bool {
	_, ok := have[want]
	return ok
}

// appendCodeownersLine appends `pattern owner` to coPath, creating a
// trailing newline if absent. Idempotent: re-reads the file first.
func appendCodeownersLine(coPath, pattern, owner string) (string, error) {
	contents, err := os.ReadFile(coPath)
	if err != nil {
		return "", fmt.Errorf("read CODEOWNERS: %w", err)
	}
	if codeownersHasPattern(codeownersPatterns(string(contents)), pattern) {
		return fmt.Sprintf("CODEOWNERS already contains %q (no change)", pattern), nil
	}
	var b strings.Builder
	b.Write(contents)
	if len(contents) > 0 && contents[len(contents)-1] != '\n' {
		b.WriteByte('\n')
	}
	line := fmt.Sprintf("%s %s\n", pattern, owner)
	b.WriteString(line)
	if err := os.WriteFile(coPath, []byte(b.String()), 0o644); err != nil {
		return "", fmt.Errorf("write CODEOWNERS: %w", err)
	}
	return fmt.Sprintf("appended %q to %s", strings.TrimRight(line, "\n"), coPath), nil
}

// requiredAllowlistEntries is the canonical seed of direct external
// dependencies that the workspace requires. Real production code
// would derive these from `go list -m all`; for kit-doctor we keep
// the list short and explicit so the fix stays safe.
var requiredAllowlistEntries = []string{
	"github.com/redis/go-redis/v9",
	"github.com/jackc/pgx/v5",
}

// checkDependencyAllowlist flags required entries missing from
// docs/audit/dependency-allowlist.txt. The fix appends them with a
// `# needs review` annotation so the operator must still triage.
func checkDependencyAllowlist(root string) ([]rules.Finding, error) {
	path := filepath.Join(root, "docs", "audit", "dependency-allowlist.txt")
	contents, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read allowlist: %w", err)
	}
	have := allowlistEntries(string(contents))
	var findings []rules.Finding
	for _, want := range requiredAllowlistEntries {
		if _, ok := have[want]; ok {
			continue
		}
		want := want
		findings = append(findings, rules.Finding{
			Rule:       "dependency-allowlist-missing",
			Severity:   rules.High,
			File:       path,
			Message:    fmt.Sprintf("required dependency %q missing from allowlist", want),
			Suggestion: fmt.Sprintf("append `%s # needs review` to %s", want, path),
			Fix: func() (string, error) {
				return appendAllowlistEntry(path, want)
			},
		})
	}
	return findings, nil
}

// allowlistEntries returns the set of module paths declared on non-
// comment, non-blank lines. The first whitespace-separated field is
// the module path; trailing tokens may be a `# comment`.
func allowlistEntries(s string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, line := range strings.Split(s, "\n") {
		trim := strings.TrimSpace(line)
		if trim == "" || strings.HasPrefix(trim, "#") {
			continue
		}
		if i := strings.IndexAny(trim, " \t#"); i >= 0 {
			trim = strings.TrimSpace(trim[:i])
		}
		if trim != "" {
			out[trim] = struct{}{}
		}
	}
	return out
}

// appendAllowlistEntry appends `pattern # needs review (kit-doctor)`
// to path. Idempotent.
func appendAllowlistEntry(path, pattern string) (string, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read allowlist: %w", err)
	}
	if _, ok := allowlistEntries(string(contents))[pattern]; ok {
		return fmt.Sprintf("allowlist already contains %q (no change)", pattern), nil
	}
	var b strings.Builder
	b.Write(contents)
	if len(contents) > 0 && contents[len(contents)-1] != '\n' {
		b.WriteByte('\n')
	}
	line := fmt.Sprintf("%s # needs review (kit-doctor)\n", pattern)
	b.WriteString(line)
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		return "", fmt.Errorf("write allowlist: %w", err)
	}
	return fmt.Sprintf("appended %q to %s", strings.TrimRight(line, "\n"), path), nil
}

// checkGoWorkSum surfaces a stale go.work.sum by running `go work
// sync` in dry-run mode against a copy; here we don't actually
// detect staleness — we just emit an actionable finding when go.work
// exists. The fix runs `go work sync` against root. Idempotent: a
// no-op sync is safe.
func checkGoWorkSum(root string) ([]rules.Finding, error) {
	workPath := filepath.Join(root, "go.work")
	if _, err := os.Stat(workPath); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("stat go.work: %w", err)
	}
	stale, err := goWorkSumLooksStale(root)
	if err != nil {
		return nil, err
	}
	if !stale {
		return nil, nil
	}
	return []rules.Finding{{
		Rule:       "go-work-sum-stale",
		Severity:   rules.High,
		File:       filepath.Join(root, "go.work.sum"),
		Message:    "go.work.sum may be stale (missing or smaller than go.sum union)",
		Suggestion: "run `go work sync` from the workspace root",
		Fix: func() (string, error) {
			return runGoWorkSync(root)
		},
	}}, nil
}

// goWorkSumLooksStale reports a heuristic: go.work.sum is missing or
// strictly smaller than the largest single go.sum in the workspace.
// This is intentionally a heuristic — the real arbiter is
// `make check-publishable` and CI. The fix is idempotent regardless.
func goWorkSumLooksStale(root string) (bool, error) {
	sumPath := filepath.Join(root, "go.work.sum")
	sumInfo, err := os.Stat(sumPath)
	if err != nil {
		if os.IsNotExist(err) {
			return true, nil
		}
		return false, err
	}
	largest := int64(0)
	walkErr := filepath.Walk(root, func(path string, info os.FileInfo, werr error) error {
		if werr != nil {
			return nil
		}
		if info.IsDir() {
			name := info.Name()
			if name == "vendor" || name == "node_modules" || strings.HasPrefix(name, ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Base(path) == "go.sum" && info.Size() > largest {
			largest = info.Size()
		}
		return nil
	})
	if walkErr != nil {
		return false, walkErr
	}
	return sumInfo.Size() < largest, nil
}

// runGoWorkSync executes `go work sync` from root. The Fix prints
// stdout/stderr in the returned summary so the operator can audit
// the exact effect.
func runGoWorkSync(root string) (string, error) {
	cmd := exec.Command("go", "work", "sync")
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("go work sync: %w: %s", err, strings.TrimSpace(string(out)))
	}
	summary := fmt.Sprintf("ran `go work sync` in %s", root)
	if s := strings.TrimSpace(string(out)); s != "" {
		summary = fmt.Sprintf("%s (output: %s)", summary, s)
	}
	return summary, nil
}

// requiredKitEnvVars lists kit-required env vars every service must
// declare in its config schema. checkServiceConfigEnvVars looks for
// a .env.example at the scan root and reports omissions. The fix
// returns a string patch (since cmd cannot edit arbitrary service
// schemas) and applies it by appending placeholder lines to the
// example file.
var requiredKitEnvVars = []string{
	"SERVER_PORT",
	"ENVIRONMENT",
	"LOG_LEVEL",
}

// checkServiceConfigEnvVars looks for `.env.example` at root and
// reports missing kit-required env vars.
func checkServiceConfigEnvVars(root string) ([]rules.Finding, error) {
	path := filepath.Join(root, ".env.example")
	contents, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read .env.example: %w", err)
	}
	have := envExampleVars(string(contents))
	var findings []rules.Finding
	for _, want := range requiredKitEnvVars {
		if _, ok := have[want]; ok {
			continue
		}
		want := want
		findings = append(findings, rules.Finding{
			Rule:       "env-example-missing-kit-required",
			Severity:   rules.High,
			File:       path,
			Message:    fmt.Sprintf("kit-required env var %q missing from .env.example", want),
			Suggestion: fmt.Sprintf("append `%s=` to %s", want, path),
			Fix: func() (string, error) {
				return appendEnvExampleLine(path, want)
			},
		})
	}
	return findings, nil
}

// envExampleVars returns the set of NAME tokens from `NAME=...`
// lines, skipping comments and blanks.
func envExampleVars(s string) map[string]struct{} {
	out := map[string]struct{}{}
	scanner := bufio.NewScanner(strings.NewReader(s))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		eq := strings.IndexByte(line, '=')
		if eq <= 0 {
			continue
		}
		out[strings.TrimSpace(line[:eq])] = struct{}{}
	}
	return out
}

// appendEnvExampleLine appends `NAME=` to path. Idempotent.
func appendEnvExampleLine(path, name string) (string, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read .env.example: %w", err)
	}
	if _, ok := envExampleVars(string(contents))[name]; ok {
		return fmt.Sprintf(".env.example already declares %s (no change)", name), nil
	}
	var b strings.Builder
	b.Write(contents)
	if len(contents) > 0 && contents[len(contents)-1] != '\n' {
		b.WriteByte('\n')
	}
	line := fmt.Sprintf("%s=\n", name)
	b.WriteString(line)
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		return "", fmt.Errorf("write .env.example: %w", err)
	}
	return fmt.Sprintf("appended %q to %s", strings.TrimRight(line, "\n"), path), nil
}
