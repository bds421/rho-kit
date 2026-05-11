package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/bds421/rho-kit/security/v2/asvs"
)

// runASVS scans path for ASVS evidence and prints a coverage report.
// FR-007 [HIGH]: the report distinguishes import-derived package
// capability evidence from comment annotations (kit-internal
// documentation only).
//
// Returns the desired exit code:
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
	imports, err := asvs.ScanImports(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "kit-doctor: asvs imports: %v\n", err)
		return 2
	}
	annotations, err := asvs.ScanDir(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "kit-doctor: asvs scan: %v\n", err)
		return 2
	}

	if format == "json" {
		_ = json.NewEncoder(os.Stdout).Encode(struct {
			Imports     asvs.ImportReport `json:"imports"`
			Annotations asvs.ScanReport   `json:"annotations"`
		}{imports, annotations})
	} else {
		printImportReport(imports)
		fmt.Println()
		printAnnotationReport(annotations)
	}

	if len(annotations.Unknown) > 0 {
		return 1
	}
	return 0
}

// printImportReport renders the package-capability view of the
// service's ASVS posture: which kit packages it imports, and what
// controls those imports make available.
func printImportReport(r asvs.ImportReport) {
	fmt.Printf("ASVS coverage from non-blank imports (package capability): %d kit-namespace imports across %d unique controls\n",
		len(r.Imports), len(r.Claimed))

	if len(r.Claimed) > 0 {
		fmt.Println("\nClaimed controls (with strongest observed evidence):")
		for _, pair := range r.EvidenceSummary() {
			c, err := asvs.Lookup(pair.ID)
			if err != nil {
				fmt.Printf("  %-10s [%s] [unknown]\n", pair.ID, pair.Evidence)
				continue
			}
			fmt.Printf("  %-10s [%s] %s — %s\n", pair.ID, pair.Evidence, c.Chapter, c.Description)
		}
	}

	if len(r.Missing) > 0 {
		fmt.Printf("\nCatalog controls without import-evidence (%d):\n", len(r.Missing))
		for _, id := range r.Missing {
			c, _ := asvs.Lookup(id)
			fmt.Printf("  %-10s %s — %s\n", id, c.Chapter, c.Description)
		}
		fmt.Println("\nA missing entry may be a control your service genuinely does not")
		fmt.Println("need (e.g. V12 file controls for a non-upload service). To gain")
		fmt.Println("import-evidence, import the matching kit package — see")
		fmt.Println("security/asvs/PackageRegistry for the full mapping.")
	}
}

// printAnnotationReport renders the comment-based view, clearly
// labeled as documentation-only so operators do not mistake it for
// compliance evidence (audit FR-007).
func printAnnotationReport(r asvs.ScanReport) {
	fmt.Printf("ASVS annotations (documentation only — DO NOT use as compliance evidence): %d annotations across %d unique IDs\n",
		len(r.Annotations), len(r.Claimed))

	if len(r.Unknown) > 0 {
		fmt.Println("\nUnknown IDs (likely typos — fix or add to security/asvs/Catalog):")
		for _, id := range r.Unknown {
			fmt.Printf("  %s\n", id)
		}
	}
}
