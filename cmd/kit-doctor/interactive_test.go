package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/cmd/kit-doctor/v2/rules"
)

// fixCounter returns a Fix function that increments calls and emits
// the configured summary; used to assert the prompt actually
// invoked the fix.
func fixCounter(calls *int, summary string) func() (string, error) {
	return func() (string, error) {
		*calls++
		return summary, nil
	}
}

// TestRunInteractive_AppliesOnY verifies "y" applies the fix and
// "n" leaves it unapplied. The default for any non-y/non-yes input
// MUST be "no".
func TestRunInteractive_AppliesOnY(t *testing.T) {
	appliedA, appliedB := 0, 0
	findings := []rules.Finding{
		{
			Rule: "rule-a", Severity: rules.High, File: "a.txt",
			Message: "thing a", Suggestion: "do a",
			Fix: fixCounter(&appliedA, "did a"),
		},
		{
			Rule: "rule-b", Severity: rules.Critical, File: "b.txt",
			Message: "thing b", Suggestion: "do b",
			Fix: fixCounter(&appliedB, "did b"),
		},
	}
	// First prompt "y", second prompt "n".
	in := strings.NewReader("y\nn\n")
	var out bytes.Buffer
	applied := runInteractive(in, &out, findings)

	assert.Equal(t, 1, applied)
	assert.Equal(t, 1, appliedA, "rule-a fix must run on y")
	assert.Equal(t, 0, appliedB, "rule-b fix must NOT run on n")
	assert.Contains(t, out.String(), "did a", "summary must reach the operator")
	assert.Contains(t, out.String(), "→ skipped", "n must show skipped marker")
}

// TestRunInteractive_DefaultIsNo verifies the explicit "any input
// other than y is no" contract: pressing Enter (empty line) MUST
// be a no.
func TestRunInteractive_DefaultIsNo(t *testing.T) {
	calls := 0
	findings := []rules.Finding{
		{Rule: "r", Severity: rules.High, File: "x", Message: "m", Fix: fixCounter(&calls, "x")},
	}
	in := strings.NewReader("\n")
	var out bytes.Buffer
	applied := runInteractive(in, &out, findings)
	assert.Equal(t, 0, applied)
	assert.Equal(t, 0, calls, "empty input must default to no")
}

// TestRunInteractive_SkipAllAbortsLoop verifies "skip-all" stops
// prompting without applying further fixes.
func TestRunInteractive_SkipAllAbortsLoop(t *testing.T) {
	callsA, callsB, callsC := 0, 0, 0
	findings := []rules.Finding{
		{Rule: "a", Severity: rules.High, File: "a", Message: "m", Fix: fixCounter(&callsA, "a")},
		{Rule: "b", Severity: rules.High, File: "b", Message: "m", Fix: fixCounter(&callsB, "b")},
		{Rule: "c", Severity: rules.High, File: "c", Message: "m", Fix: fixCounter(&callsC, "c")},
	}
	in := strings.NewReader("y\nskip-all\nyes\n")
	var out bytes.Buffer
	applied := runInteractive(in, &out, findings)
	assert.Equal(t, 1, applied, "only the pre-skip-all fix should run")
	assert.Equal(t, 1, callsA)
	assert.Equal(t, 0, callsB)
	assert.Equal(t, 0, callsC)
	assert.Contains(t, out.String(), "skip-all")
}

// TestRunInteractive_PrintsFindingsWithoutFix verifies the loop never
// prompts for a finding whose Fix is nil, but DOES print it: repo
// findings are not in the standard text/json output, so a Fix-less one
// must still be visible here or it would drive exit-1 with no cause.
func TestRunInteractive_PrintsFindingsWithoutFix(t *testing.T) {
	calls := 0
	findings := []rules.Finding{
		{Rule: "info-only", Severity: rules.Info, File: "x", Message: "m", Suggestion: "look here"},
		{Rule: "fixable", Severity: rules.High, File: "y", Message: "m", Fix: fixCounter(&calls, "ok")},
	}
	in := strings.NewReader("y\n")
	var out bytes.Buffer
	applied := runInteractive(in, &out, findings)
	assert.Equal(t, 1, applied)
	assert.Equal(t, 1, calls)
	got := out.String()
	// The non-fixable finding is printed so its exit-1 cause is visible...
	assert.Contains(t, got, "info-only", "non-fixable findings must still be shown")
	assert.Contains(t, got, "look here", "its suggestion must be shown")
	// ...but only the fixable one gets an apply prompt.
	assert.Equal(t, 1, strings.Count(got, "apply? [y/N/skip-all]"),
		"only the fixable finding may be prompted")
}

// TestRunInteractive_FixErrorContinuesLoop verifies a failing Fix
// is reported but does not abort the remaining prompts.
func TestRunInteractive_FixErrorContinuesLoop(t *testing.T) {
	bCalls := 0
	findings := []rules.Finding{
		{
			Rule: "a", Severity: rules.High, File: "a", Message: "m",
			Fix: func() (string, error) { return "", assertError("kaboom") },
		},
		{Rule: "b", Severity: rules.High, File: "b", Message: "m", Fix: fixCounter(&bCalls, "ok")},
	}
	in := strings.NewReader("y\ny\n")
	var out bytes.Buffer
	applied := runInteractive(in, &out, findings)
	assert.Equal(t, 1, applied, "only the second fix succeeded")
	assert.Equal(t, 1, bCalls)
	assert.Contains(t, out.String(), "fix failed")
}

// TestRunInteractive_PipedStdinIntegration is the integration test
// required by the wave brief: drives the prompt with a piped stdin
// reader and asserts the human-readable prompt shape.
func TestRunInteractive_PipedStdinIntegration(t *testing.T) {
	calls := 0
	findings := []rules.Finding{
		{
			Rule: "codeowners-missing-security-path", Severity: rules.High,
			File: "/repo/.github/CODEOWNERS", Line: 0,
			Message:    `security-sensitive path "security/" has no CODEOWNERS entry`,
			Suggestion: "append `security/ @security-review` to CODEOWNERS",
			Fix:        fixCounter(&calls, "appended security/ @security-review to /repo/.github/CODEOWNERS"),
		},
	}
	in := strings.NewReader("y\n")
	var out bytes.Buffer
	runInteractive(in, &out, findings)
	got := out.String()

	require.Equal(t, 1, calls)
	// The prompt shape pinned by the wave brief.
	assert.Contains(t, got, "[HIGH] codeowners-missing-security-path:")
	assert.Contains(t, got, "at /repo/.github/CODEOWNERS")
	assert.Contains(t, got, "suggested fix: append `security/ @security-review`")
	assert.Contains(t, got, "apply? [y/N/skip-all]")
	assert.Contains(t, got, "appended security/ @security-review")
}

// TestRunInteractive_EOFTreatedAsNo verifies that an unexpectedly
// closed stdin (no input at all) does not panic and does not apply
// any fix.
func TestRunInteractive_EOFTreatedAsNo(t *testing.T) {
	calls := 0
	findings := []rules.Finding{
		{Rule: "r", Severity: rules.High, File: "x", Message: "m", Fix: fixCounter(&calls, "x")},
	}
	in := strings.NewReader("")
	var out bytes.Buffer
	applied := runInteractive(in, &out, findings)
	assert.Equal(t, 0, applied)
	assert.Equal(t, 0, calls)
}

// TestRunInteractiveSession_AppliedFixesDropFromExit verifies that a
// finding the operator successfully fixed is NOT reported as
// unresolved, so it cannot keep driving exit-1. This guards the
// reported defect: applying every fix must let the run exit 0.
func TestRunInteractiveSession_AppliedFixesDropFromExit(t *testing.T) {
	appliedA, appliedB := 0, 0
	findings := []rules.Finding{
		{Rule: "a", Severity: rules.High, File: "a", Message: "m", Fix: fixCounter(&appliedA, "did a")},
		{Rule: "b", Severity: rules.High, File: "b", Message: "m", Fix: fixCounter(&appliedB, "did b")},
	}
	in := strings.NewReader("y\ny\n")
	var out bytes.Buffer
	res := runInteractiveSession(in, &out, findings)

	assert.Equal(t, 2, res.applied)
	assert.Empty(t, res.unresolved, "fixes that were applied must not count toward exit")
	// The pre-fix findings would all be HIGH; the unresolved set must
	// not trip the default exit floor.
	assert.Equal(t, 0, exitCode(res.unresolved, rules.High))
}

// TestRunInteractiveSession_DeclinedFixStaysUnresolved verifies a
// finding the operator declined still counts toward exit-1.
func TestRunInteractiveSession_DeclinedFixStaysUnresolved(t *testing.T) {
	appliedA, appliedB := 0, 0
	findings := []rules.Finding{
		{Rule: "a", Severity: rules.High, File: "a", Message: "m", Fix: fixCounter(&appliedA, "did a")},
		{Rule: "b", Severity: rules.High, File: "b", Message: "m", Fix: fixCounter(&appliedB, "did b")},
	}
	// Apply the first, decline the second.
	in := strings.NewReader("y\nn\n")
	var out bytes.Buffer
	res := runInteractiveSession(in, &out, findings)

	assert.Equal(t, 1, res.applied)
	require.Len(t, res.unresolved, 1)
	assert.Equal(t, "b", res.unresolved[0].Rule, "declined finding must remain unresolved")
	assert.Equal(t, 1, exitCode(res.unresolved, rules.High))
}

// TestRunInteractiveSession_FailedFixStaysUnresolved verifies a Fix
// that errored is still counted toward exit-1 (not silently dropped).
func TestRunInteractiveSession_FailedFixStaysUnresolved(t *testing.T) {
	findings := []rules.Finding{
		{
			Rule: "a", Severity: rules.High, File: "a", Message: "m",
			Fix: func() (string, error) { return "", assertError("kaboom") },
		},
	}
	in := strings.NewReader("y\n")
	var out bytes.Buffer
	res := runInteractiveSession(in, &out, findings)

	assert.Equal(t, 0, res.applied)
	require.Len(t, res.unresolved, 1)
	assert.Equal(t, "a", res.unresolved[0].Rule)
}

// TestRunInteractiveSession_SkipAllKeepsRemainingUnresolved verifies
// that skip-all leaves the not-yet-prompted findings unresolved so
// they still drive exit-1.
func TestRunInteractiveSession_SkipAllKeepsRemainingUnresolved(t *testing.T) {
	a, b, c := 0, 0, 0
	findings := []rules.Finding{
		{Rule: "a", Severity: rules.High, File: "a", Message: "m", Fix: fixCounter(&a, "a")},
		{Rule: "b", Severity: rules.High, File: "b", Message: "m", Fix: fixCounter(&b, "b")},
		{Rule: "c", Severity: rules.High, File: "c", Message: "m", Fix: fixCounter(&c, "c")},
	}
	in := strings.NewReader("y\nskip-all\n")
	var out bytes.Buffer
	res := runInteractiveSession(in, &out, findings)

	assert.Equal(t, 1, res.applied)
	require.Len(t, res.unresolved, 2, "b and c were never resolved")
	assert.Equal(t, "b", res.unresolved[0].Rule)
	assert.Equal(t, "c", res.unresolved[1].Rule)
}

// TestRunInteractiveSession_NonFixableStaysUnresolved verifies a
// finding without a Fix is never resolvable and keeps counting toward
// exit-1 (interactive mode cannot make it go away).
func TestRunInteractiveSession_NonFixableStaysUnresolved(t *testing.T) {
	findings := []rules.Finding{
		{Rule: "info", Severity: rules.High, File: "x", Message: "m"},
	}
	in := strings.NewReader("")
	var out bytes.Buffer
	res := runInteractiveSession(in, &out, findings)

	assert.Equal(t, 0, res.applied)
	require.Len(t, res.unresolved, 1)
	assert.Equal(t, "info", res.unresolved[0].Rule)
}

// TestRunInteractiveSession_RepoCheckErrorIsVisible guards the
// reported defect: a Fix-less repo-check-error Warning is excluded
// from the standard text/json output yet still drives exit-1, so it
// must be printed in the interactive session (without a prompt) to
// explain why the run did not exit 0.
func TestRunInteractiveSession_RepoCheckErrorIsVisible(t *testing.T) {
	findings := []rules.Finding{
		{
			Rule: "repo-check-error", Severity: rules.Warning,
			File: "/repo", Message: "repo check failed: stat go.work: boom",
		},
	}
	var out bytes.Buffer
	res := runInteractiveSession(strings.NewReader(""), &out, findings)

	require.Len(t, res.unresolved, 1, "the error still counts toward exit-1")
	got := out.String()
	assert.Contains(t, got, "repo-check-error",
		"a Fix-less repo finding driving exit-1 must be visible")
	assert.Contains(t, got, "repo check failed: stat go.work: boom")
	assert.NotContains(t, got, "apply?", "non-fixable findings must not be prompted")
	// With -strict=warning this unresolved Warning trips exit-1; the
	// operator can now see the cause printed above.
	assert.Equal(t, 1, exitCode(res.unresolved, rules.Warning))
}

// assertError is a tiny error type for tests so we don't import
// errors just to express a single sentinel.
type assertError string

func (e assertError) Error() string { return string(e) }
