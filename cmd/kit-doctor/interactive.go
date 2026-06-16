// Interactive mode prompts the operator for each fixable finding
// with a y/N/skip-all prompt. Default is "no": any input other than
// "y"/"yes" (case-insensitive) is treated as "no". The special
// token "skip-all" aborts interactive prompting without exiting the
// process so the standard text output and exit code still happen.
//
// Output is human-only. Combining -interactive with -format=json is
// rejected at flag parse time.
package main

import (
	"bufio"
	"fmt"
	"io"
	"strings"

	"github.com/bds421/rho-kit/cmd/kit-doctor/v2/rules"
)

// interactiveResult reports the outcome of an interactive session.
//
// applied is the number of fixes the operator successfully applied.
//
// unresolved holds the findings interactive mode did NOT clear:
// findings without a Fix, findings the operator declined, findings
// whose Fix errored, and every finding still pending when the operator
// answered "skip-all" or closed stdin. The caller computes the exit
// code from unresolved so successfully applied fixes stop driving
// exit-1, while declined/failed ones keep surfacing through it.
type interactiveResult struct {
	applied    int
	unresolved []rules.Finding
}

// runInteractive prompts the operator for each finding that carries
// a Fix function. Findings without a Fix are printed without a prompt:
// repo-level findings (the input to interactive mode) are NOT part of
// the standard text/json output, so a Fix-less one — e.g. a
// repo-check-error Warning — would otherwise drive exit-1 with no
// visible cause.
//
// in is the prompt input source (os.Stdin in production; a piped
// reader in tests). out is the prompt output sink (os.Stdout in
// production).
//
// Returns the count of fixes applied. Errors from individual Fix
// calls are printed but do NOT abort the loop — the operator can
// still apply the remaining fixes.
func runInteractive(in io.Reader, out io.Writer, findings []rules.Finding) int {
	return runInteractiveSession(in, out, findings).applied
}

// runInteractiveSession runs the interactive prompt loop and reports
// both the applied-fix count and the findings that remain unresolved.
//
// It carries the same prompt contract as the original loop: the only
// difference is that it also tracks which findings interactive mode
// could not clear so the caller can keep them — and only them —
// counting toward the exit code.
func runInteractiveSession(in io.Reader, out io.Writer, findings []rules.Finding) interactiveResult {
	reader := bufio.NewReader(in)
	res := interactiveResult{}
	for i, f := range findings {
		if f.Fix == nil {
			// Not fixable in interactive mode; interactive cannot
			// clear it, so it keeps counting toward the exit code.
			// Print it (without a prompt) so the operator can see what
			// is keeping the run from exiting 0 — repo findings never
			// reach the standard text/json output.
			printFinding(out, f)
			res.unresolved = append(res.unresolved, f)
			continue
		}
		printFinding(out, f)
		writef(out, "  apply? [y/N/skip-all] ")

		ans, err := readAnswer(reader)
		if err != nil {
			writef(out, "\n  (input closed; treating as no)\n")
			// This finding and every one after it stays unresolved.
			res.unresolved = append(res.unresolved, findings[i:]...)
			return res
		}
		switch ans {
		case "skip-all":
			writef(out, "  → skip-all: aborting interactive prompts\n")
			// This finding and every one after it stays unresolved.
			res.unresolved = append(res.unresolved, findings[i:]...)
			return res
		case "y", "yes":
			summary, err := f.Fix()
			if err != nil {
				writef(out, "  ✗ fix failed: %v\n", err)
				res.unresolved = append(res.unresolved, f)
				continue
			}
			writef(out, "  ✓ %s\n", summary)
			res.applied++
		default:
			writef(out, "  → skipped\n")
			res.unresolved = append(res.unresolved, f)
		}
	}
	return res
}

// printFinding writes the human-readable header, location, and (if
// present) suggested fix for a single finding to out. It does NOT
// write the apply prompt — callers add that only for fixable findings.
func printFinding(out io.Writer, f rules.Finding) {
	writef(out, "\n[%s] %s: %s\n", f.Severity, f.Rule, f.Message)
	if f.File != "" {
		writef(out, "  at %s", f.File)
		if f.Line > 0 {
			writef(out, ":%d", f.Line)
		}
		writef(out, "\n")
	}
	if f.Suggestion != "" {
		writef(out, "  suggested fix: %s\n", f.Suggestion)
	}
}

// writef is a fmt.Fprintf wrapper that intentionally discards both
// return values. The prompt is best-effort: a broken stdout is not
// recoverable in interactive mode and would surface as the next
// write failing anyway.
func writef(out io.Writer, format string, args ...any) {
	_, _ = fmt.Fprintf(out, format, args...)
}

// readAnswer reads one line from reader, trims whitespace, and
// lower-cases the result. Returns io.EOF unchanged so the caller
// can distinguish "stdin closed" from "operator pressed enter".
func readAnswer(reader *bufio.Reader) (string, error) {
	line, err := reader.ReadString('\n')
	line = strings.ToLower(strings.TrimSpace(line))
	if err != nil && err != io.EOF {
		return "", err
	}
	if err == io.EOF && line == "" {
		return "", io.EOF
	}
	return line, nil
}
