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

// runInteractive prompts the operator for each finding that carries
// a Fix function. Findings without Fix are skipped silently — they
// already appear in the standard text output.
//
// in is the prompt input source (os.Stdin in production; a piped
// reader in tests). out is the prompt output sink (os.Stdout in
// production).
//
// Returns the count of fixes applied. Errors from individual Fix
// calls are printed but do NOT abort the loop — the operator can
// still apply the remaining fixes.
func runInteractive(in io.Reader, out io.Writer, findings []rules.Finding) int {
	reader := bufio.NewReader(in)
	applied := 0
	for _, f := range findings {
		if f.Fix == nil {
			continue
		}
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
		writef(out, "  apply? [y/N/skip-all] ")

		ans, err := readAnswer(reader)
		if err != nil {
			writef(out, "\n  (input closed; treating as no)\n")
			return applied
		}
		switch ans {
		case "skip-all":
			writef(out, "  → skip-all: aborting interactive prompts\n")
			return applied
		case "y", "yes":
			summary, err := f.Fix()
			if err != nil {
				writef(out, "  ✗ fix failed: %v\n", err)
				continue
			}
			writef(out, "  ✓ %s\n", summary)
			applied++
		default:
			writef(out, "  → skipped\n")
		}
	}
	return applied
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
