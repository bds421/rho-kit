// Command kit-contract validates portable rho-kit contract artifacts and
// compares a candidate bundle with a baseline bundle for backward
// compatibility. It is intentionally local/CI-friendly: publishing to a
// remote registry is outside the v1 scope.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/bds421/rho-kit/cmd/kit-contract/v2/contract"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		usage(stderr)
		return 2
	}
	switch args[0] {
	case "validate":
		fs := flag.NewFlagSet("validate", flag.ContinueOnError)
		fs.SetOutput(stderr)
		dir := fs.String("dir", ".", "Directory containing contracts.json")
		if err := fs.Parse(args[1:]); err != nil {
			return 2
		}
		if err := contract.ValidateDir(*dir); err != nil {
			if _, writeErr := fmt.Fprintf(stderr, "kit-contract: validate: %v\n", err); writeErr != nil {
				return 1
			}
			return 1
		}
		if _, err := fmt.Fprintln(stdout, "OK: contract bundle is valid"); err != nil {
			return 1
		}
		return 0
	case "compare":
		fs := flag.NewFlagSet("compare", flag.ContinueOnError)
		fs.SetOutput(stderr)
		candidate := fs.String("candidate", ".", "Candidate contract bundle directory")
		baseline := fs.String("baseline", "", "Baseline contract bundle directory")
		format := fs.String("format", "text", "Output format: text | json")
		if err := fs.Parse(args[1:]); err != nil {
			return 2
		}
		if *baseline == "" {
			if _, err := fmt.Fprintln(stderr, "kit-contract: compare: -baseline is required"); err != nil {
				return 1
			}
			return 2
		}
		report, err := contract.CompareDirs(*candidate, *baseline)
		if err != nil {
			if _, writeErr := fmt.Fprintf(stderr, "kit-contract: compare: %v\n", err); writeErr != nil {
				return 1
			}
			return 1
		}
		switch *format {
		case "text":
			for _, finding := range report.Findings {
				state := "BREAKING"
				if finding.Waived {
					state = "WAIVED"
				}
				if _, err := fmt.Fprintf(stdout, "%s %s %s: %s\n", state, finding.Artifact, finding.Code, finding.Message); err != nil {
					return 1
				}
			}
			if report.Compatible {
				if _, err := fmt.Fprintln(stdout, "OK: compatible"); err != nil {
					return 1
				}
			} else {
				if _, err := fmt.Fprintln(stdout, "INCOMPATIBLE"); err != nil {
					return 1
				}
			}
		case "json":
			if err := json.NewEncoder(stdout).Encode(report); err != nil {
				if _, writeErr := fmt.Fprintf(stderr, "kit-contract: encode report: %v\n", err); writeErr != nil {
					return 1
				}
				return 1
			}
		default:
			if _, err := fmt.Fprintf(stderr, "kit-contract: compare: unknown -format %q (text|json)\n", *format); err != nil {
				return 1
			}
			return 2
		}
		if report.Compatible {
			return 0
		}
		return 1
	default:
		usage(stderr)
		return 2
	}
}

func usage(w io.Writer) {
	_, _ = fmt.Fprintln(w, "Usage:")
	_, _ = fmt.Fprintln(w, "  kit-contract validate [-dir DIR]")
	_, _ = fmt.Fprintln(w, "  kit-contract compare -baseline DIR [-candidate DIR] [-format text|json]")
}
