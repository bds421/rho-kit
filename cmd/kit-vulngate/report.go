package main

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
)

// report is the part of grype's JSON output (-o json) the gate reads.
type report struct {
	Matches []struct {
		Vulnerability          vulnerability   `json:"vulnerability"`
		RelatedVulnerabilities []vulnerability `json:"relatedVulnerabilities"`
		Artifact               struct {
			Name    string `json:"name"`
			Version string `json:"version"`
			Type    string `json:"type"`
		} `json:"artifact"`
	} `json:"matches"`
	Source struct {
		Target json.RawMessage `json:"target"`
	} `json:"source"`
}

type vulnerability struct {
	ID             string `json:"id"`
	Severity       string `json:"severity"`
	KnownExploited []struct {
		CVE string `json:"cve"`
	} `json:"knownExploited"`
	Fix struct {
		State     string   `json:"state"`
		Versions  []string `json:"versions"`
		Available []struct {
			Version string `json:"version"`
			Date    string `json:"date"`
		} `json:"available"`
	} `json:"fix"`
}

// finding is one vulnerable package in one scanned target.
type finding struct {
	Target, ID, Severity, Package, Version, FixedIn string
	// Image is the scanned image, when the report or the caller names it.
	Image     image
	Exploited bool
	// FixedSince is when the earliest fix was published; zero when there
	// is none or its date is unknown.
	FixedSince time.Time
	Fixed      bool
}

func readReport(r io.Reader, target string, im image) ([]finding, error) {
	var rep report
	if err := json.NewDecoder(r).Decode(&rep); err != nil {
		return nil, fmt.Errorf("%s: not a grype JSON report: %w", target, err)
	}
	if im == (image{}) {
		im = scannedImage(rep.Source.Target)
	}
	out := make([]finding, 0, len(rep.Matches))
	for _, m := range rep.Matches {
		v := m.Vulnerability
		f := finding{Target: target, Image: im, ID: v.ID, Severity: v.Severity, Package: m.Artifact.Name, Version: m.Artifact.Version, Exploited: len(v.KnownExploited) > 0}
		for _, related := range m.RelatedVulnerabilities {
			if rank(related.Severity) > rank(f.Severity) {
				f.Severity = related.Severity
			}
			f.Exploited = f.Exploited || len(related.KnownExploited) > 0
		}
		if rank(f.Severity) == 0 {
			f.Severity = "unknown"
		}
		f.Fixed = v.Fix.State == "fixed"
		if len(v.Fix.Versions) > 0 {
			f.FixedIn = v.Fix.Versions[0]
		}
		for _, a := range v.Fix.Available {
			if at, err := time.Parse(time.DateOnly, a.Date); err == nil && (f.FixedSince.IsZero() || at.Before(f.FixedSince)) {
				f.FixedSince = at
			}
		}
		out = append(out, f)
	}
	return out, nil
}

// scannedImage is the image a registry scan names by digest
// (grype registry:name@sha256:…); an archive or SBOM scan names none.
func scannedImage(target json.RawMessage) image {
	var t struct {
		UserInput string `json:"userInput"`
	}
	if json.Unmarshal(target, &t) != nil {
		return image{}
	}
	im, _ := parseImage(strings.TrimPrefix(t.UserInput, "registry:"))
	return im
}
