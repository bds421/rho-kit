// Command kit-doctor is the programmatic version of the rho-kit
// security audit. Run it against a service's source tree and it
// emits findings for dangerous defaults that the audit identified.
//
// Usage:
//
//	kit-doctor [-strict=high|critical] [-format=text|json] [-interactive] PATH
//
// Exit codes:
//   - 0: no findings at or above -strict.
//   - 1: at least one finding at or above -strict.
//   - 2: tool error (bad path, IO failure).
//
// Add a rule by writing one file under ./rules/ implementing
// `rules.Rule` and registering it in `rules.Registered`. See
// `rules/jwt_missing_claims.go` for a template.
//
// Interactive mode (-interactive) runs the scan as usual and then,
// for each finding that carries a Fix function, prompts the operator
// to apply it. Interactive mode is incompatible with -format=json
// because prompts must reach a human. See ./interactive.go for the
// prompt contract.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/bds421/rho-kit/cmd/kit-doctor/v2/rules"
)

func main() {
	strict := flag.String("strict", "high", "exit-1 floor: critical|high|warning|info")
	format := flag.String("format", "text", "output format: text|json")
	asvsMode := flag.Bool("asvs", false, "scan for ASVS annotations and emit a coverage report instead of running the rule set")
	interactive := flag.Bool("interactive", false, "after scan, prompt to apply each fixable finding (text format only)")
	flag.Parse()

	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: kit-doctor [-strict=...] [-format=...] [-asvs] [-interactive] PATH")
		os.Exit(2)
	}
	path := flag.Arg(0)

	if *interactive && *format == "json" {
		fmt.Fprintln(os.Stderr, "kit-doctor: -interactive is incompatible with -format=json (prompts are human-only)")
		os.Exit(2)
	}

	if *asvsMode {
		os.Exit(runASVS(path, *format))
	}

	floor, err := parseSeverity(*strict)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}

	findings, err := scan(path, rules.Registered())
	if err != nil {
		fmt.Fprintf(os.Stderr, "kit-doctor: scan: %v\n", err)
		os.Exit(2)
	}

	switch *format {
	case "json":
		_ = json.NewEncoder(os.Stdout).Encode(findings)
	default:
		fmt.Print(formatFindings(findings))
	}

	// Interactive mode runs additional repo-level checks whose
	// findings carry Fix functions. These do NOT appear in the
	// standard text/json output so the non-interactive contract
	// stays byte-for-byte identical.
	if *interactive {
		repoFindings := runRepoCheckers(path, repoCheckers())
		runInteractive(os.Stdin, os.Stdout, repoFindings)
		// Repo findings still count toward exit code so a missing
		// CODEOWNERS entry that the operator declined to fix is
		// surfaced through exit-1.
		if exitCode(repoFindings, floor) == 1 {
			os.Exit(1)
		}
	}
	os.Exit(exitCode(findings, floor))
}

func parseSeverity(s string) (rules.Severity, error) {
	switch s {
	case "critical":
		return rules.Critical, nil
	case "high":
		return rules.High, nil
	case "warning":
		return rules.Warning, nil
	case "info":
		return rules.Info, nil
	}
	return 0, fmt.Errorf("kit-doctor: -strict must be critical|high|warning|info")
}
