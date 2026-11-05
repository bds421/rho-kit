// Command kit-vulngate holds a release to the patch deadlines. It reads
// grype JSON reports (grype <target> -o json), one per image or SBOM that
// ships, and lists every vulnerability with its deadline, counted from the
// day its fix was published: CISA KEV 72 hours, critical 7 days, high 30,
// medium 90 (also without a severity), low by the next release. It exits 1
// when a fixed one is past its deadline and not accepted, 2 on bad input.
// A vulnerability without a fix is listed, never failed.
//
//	kit-vulngate [-accepted security/accepted-vulnerabilities.json] report.json...
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, time.Now()))
}

func run(args []string, stdout, stderr io.Writer, now time.Time) int {
	flags := flag.NewFlagSet("kit-vulngate", flag.ContinueOnError)
	flags.SetOutput(stderr)
	acceptedFile := flags.String("accepted", "", "JSON list of accepted findings (id, package, reason, until)")
	if err := flags.Parse(args); err != nil || flags.NArg() == 0 {
		_, _ = fmt.Fprintln(stderr, "usage: kit-vulngate [-accepted file] grype-report.json...")
		return 2
	}
	list, err := acceptances(*acceptedFile)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 2
	}
	var all []finding
	for _, path := range flags.Args() {
		found, err := readFile(path)
		if err != nil {
			_, _ = fmt.Fprintln(stderr, err)
			return 2
		}
		all = append(all, found...)
	}
	late := judge(stdout, all, list, now)
	if late > 0 {
		_, _ = fmt.Fprintf(stdout, "%d past their patch deadline\n", late)
		return 1
	}
	return 0
}

func acceptances(path string) ([]acceptance, error) {
	if path == "" {
		return nil, nil
	}
	f, err := os.Open(path) //nolint:gosec // the caller names the file
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return readAccepted(f)
}

func readFile(path string) ([]finding, error) {
	f, err := os.Open(path) //nolint:gosec // the caller names the reports
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return readReport(f, strings.TrimSuffix(filepath.Base(path), ".json"))
}

// judge prints each finding, most urgent first, and counts the late ones.
func judge(w io.Writer, all []finding, list []acceptance, now time.Time) int {
	slices.SortFunc(all, func(a, b finding) int {
		if d := rank(b.Severity) - rank(a.Severity); d != 0 {
			return d
		}
		return strings.Compare(a.ID+a.Target, b.ID+b.Target)
	})
	late := 0
	for _, f := range all {
		state := status(f, list, now)
		if strings.HasPrefix(state, "LATE") {
			late++
		}
		exploited := ""
		if f.Exploited {
			exploited = " KEV"
		}
		_, _ = fmt.Fprintf(w, "%-8s %-20s%s %s %s -> %s  [%s]  %s\n", strings.ToUpper(f.Severity), f.ID, exploited, f.Package, f.Version, orNone(f.FixedIn), f.Target, state)
	}
	return late
}

func status(f finding, list []acceptance, now time.Time) string {
	switch {
	case accepted(list, f, now):
		return "accepted"
	case !f.Fixed:
		return "no fix yet"
	case f.FixedSince.IsZero():
		return "LATE: a fix exists, its date is unknown"
	}
	due := f.FixedSince.Add(deadline(f.Severity, f.Exploited))
	if !now.Before(due) {
		return "LATE: due " + due.Format(time.DateOnly)
	}
	return "due " + due.Format(time.DateOnly)
}

func orNone(v string) string {
	if v == "" {
		return "no fix"
	}
	return v
}
