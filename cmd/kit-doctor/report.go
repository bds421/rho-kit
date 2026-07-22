package main

import (
	"time"

	"github.com/bds421/rho-kit/cmd/kit-doctor/v2/rules"
)

// report is the stable machine-readable contract for CI. Findings retain their
// existing fields while the envelope permits additive metadata without making
// consumers infer scan scope from process arguments.
type report struct {
	SchemaVersion string          `json:"schema_version"`
	ScannedAt     string          `json:"scanned_at"`
	Path          string          `json:"path"`
	Strict        string          `json:"strict"`
	Findings      []rules.Finding `json:"findings"`
	Suppressions  []suppression   `json:"suppressions"`
	Summary       reportSummary   `json:"summary"`
}

type reportSummary struct {
	Info     int `json:"info"`
	Warning  int `json:"warning"`
	High     int `json:"high"`
	Critical int `json:"critical"`
}

func newReport(path, strict string, findings []rules.Finding, suppressions []suppression) report {
	r := report{SchemaVersion: "rho-kit-doctor/v1", ScannedAt: time.Now().UTC().Format(time.RFC3339), Path: path, Strict: strict, Findings: append([]rules.Finding{}, findings...), Suppressions: append([]suppression{}, suppressions...)}
	for _, finding := range findings {
		switch finding.Severity {
		case rules.Info:
			r.Summary.Info++
		case rules.Warning:
			r.Summary.Warning++
		case rules.High:
			r.Summary.High++
		case rules.Critical:
			r.Summary.Critical++
		}
	}
	return r
}
