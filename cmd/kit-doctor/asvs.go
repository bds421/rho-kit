package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/bds421/rho-kit/security/asvs"
)

// runASVS scans path for ASVS annotations and prints a coverage
// report. Returns the desired exit code:
//
//   - 0: every annotation resolves to a [asvs.Catalog] entry. (Missing
//     coverage is reported but not failed — services that genuinely
//     don't need V12 file controls shouldn't fail because they don't
//     handle uploads.)
//   - 1: at least one annotation references an unknown ID (likely typo).
//   - 2: filesystem error.
//
// Future evolution (v2.x): add a per-service expected-coverage profile
// (e.g. "this service must satisfy V2, V3, V4, V13") and fail when the
// claimed set is a strict subset of the expected set.
func runASVS(path, format string) int {
	report, err := asvs.ScanDir(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "kit-doctor: asvs scan: %v\n", err)
		return 2
	}

	if format == "json" {
		_ = json.NewEncoder(os.Stdout).Encode(report)
	} else {
		printASVSReport(report)
	}

	if len(report.Unknown) > 0 {
		return 1
	}
	return 0
}

func printASVSReport(r asvs.ScanReport) {
	fmt.Printf("ASVS scan: %d annotations across %d unique IDs\n",
		len(r.Annotations), len(r.Claimed))

	if len(r.Claimed) > 0 {
		fmt.Println("\nClaimed controls:")
		for _, id := range r.Claimed {
			c, err := asvs.Lookup(id)
			if err != nil {
				fmt.Printf("  %-10s [unknown]\n", id)
				continue
			}
			fmt.Printf("  %-10s %s — %s\n", id, c.Chapter, c.Description)
		}
	}

	if len(r.Unknown) > 0 {
		fmt.Println("\nUnknown IDs (likely typos — fix or add to security/asvs/Catalog):")
		for _, id := range r.Unknown {
			fmt.Printf("  %s\n", id)
		}
	}

	if len(r.Missing) > 0 {
		fmt.Printf("\nCatalog controls without an annotation in this tree (%d):\n", len(r.Missing))
		for _, id := range r.Missing {
			c, _ := asvs.Lookup(id)
			fmt.Printf("  %-10s %s — %s\n", id, c.Chapter, c.Description)
		}
		fmt.Println("\nA missing entry is not a hard failure — it may be a control your service")
		fmt.Println("genuinely does not need (e.g. V12 file controls for a non-upload service).")
		fmt.Println("Add `// asvs: <ID>` to the relevant package to claim coverage.")
	}
}
