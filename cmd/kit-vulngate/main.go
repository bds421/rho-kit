// Command kit-vulngate holds a release to the patch deadlines. It reads
// grype JSON reports (grype <target> -o json), one per image or SBOM that
// ships, and lists every vulnerability with its owner and deadline,
// counted from the day its fix was published: CISA KEV 72 hours, critical
// 7 days, high 30, medium, low and without a severity 90; a -policy file
// sets other deadlines and names the third-party components.
//
// It exits 1 when a fixed finding is past its deadline and not accepted
// in what we own (all that no third-party component holds), or in a
// third-party component that the -baseline of the previous release does
// not carry it in; 2 on bad input, a missing named file included. A
// finding the previous release already carried in a third-party component
// is tracked, not failed: its deadline counts from when that component
// ships a fix, which the reports cannot tell. A vulnerability without a
// fix is listed, never failed. -record writes this run's third-party
// findings as the next release's baseline, when it passes.
//
// -report lists what is deployed instead of gating a release: every
// third-party finding is tracked, and it exits 1 when one of ours is past
// its deadline, for a monitor to notify; -record then writes the
// deployed release's baseline.
//
// An acceptance with an image applies only to reports of that image: a
// registry scan by digest names it, and image=report.json names it for a
// report of an archive built from it.
//
//	kit-vulngate [-policy file] [-accepted file] [-baseline file] [-record file] [-report] [image=]report.json...
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, time.Now()))
}

const usage = "usage: kit-vulngate [-policy file] [-accepted file] [-baseline file] [-record file] [-report] [image=]grype-report.json..."

func run(args []string, stdout, stderr io.Writer, now time.Time) int {
	flags := flag.NewFlagSet("kit-vulngate", flag.ContinueOnError)
	flags.SetOutput(stderr)
	policyFile := flags.String("policy", "", "JSON deadlines and third-party components")
	acceptedFile := flags.String("accepted", "", "JSON list of accepted findings (id, package, image, reason, until)")
	baselineFile := flags.String("baseline", "", "JSON third-party findings of the previous release (as -record writes them)")
	recordFile := flags.String("record", "", "write this run's third-party findings here, for the next release's -baseline")
	reportMode := flags.Bool("report", false, "list what is deployed: track every third-party finding, fail only ours")
	if err := flags.Parse(args); err != nil || flags.NArg() == 0 {
		_, _ = fmt.Fprintln(stderr, usage)
		return 2
	}
	j, err := load(*policyFile, *acceptedFile, *baselineFile)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 2
	}
	j.now, j.deployed = now, *reportMode
	var all []finding
	seen := map[string]bool{}
	for _, arg := range flags.Args() {
		found, err := readArg(arg, seen)
		if err != nil {
			_, _ = fmt.Fprintln(stderr, err)
			return 2
		}
		all = append(all, found...)
	}
	for i := range all {
		all[i].Component = j.policy.componentOf(all[i])
	}
	late := j.list(stdout, all)
	if late > 0 {
		_, _ = fmt.Fprintf(stdout, "%d past their patch deadline\n", late)
	}
	if *recordFile != "" && (late == 0 || j.deployed) {
		if err := record(*recordFile, all); err != nil {
			_, _ = fmt.Fprintln(stderr, err)
			return 2
		}
	}
	if late > 0 {
		return 1
	}
	return 0
}

// load reads the named files; one not named is the default, one named
// and missing is an error.
func load(policyPath, acceptedPath, baselinePath string) (judge, error) {
	j := judge{policy: defaultPolicy()}
	var err error
	if policyPath != "" {
		err = readFile(policyPath, func(r io.Reader) (e error) { j.policy, e = readPolicy(r); return e })
	}
	if err == nil && acceptedPath != "" {
		err = readFile(acceptedPath, func(r io.Reader) (e error) { j.accepted, e = readAccepted(r); return e })
	}
	if err == nil && baselinePath != "" {
		err = readFile(baselinePath, func(r io.Reader) (e error) { j.baseline, e = readBaseline(r); return e })
	}
	return j, err
}

func readFile(path string, read func(io.Reader) error) error {
	f, err := os.Open(path) //nolint:gosec // the caller names the file
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	return read(f)
}

// readArg reads report.json, or image=report.json for a report of an
// archive built from that image. Two reports of one name are refused, as
// the findings would be listed as one target.
func readArg(arg string, seen map[string]bool) ([]finding, error) {
	path, im := arg, image{}
	if ref, file, ok := strings.Cut(arg, "="); ok {
		parsed, valid := parseImage(ref)
		if !valid {
			return nil, fmt.Errorf("%s: %q is not name[:tag]@sha256:<digest>", file, ref)
		}
		path, im = file, parsed
	}
	target := strings.TrimSuffix(filepath.Base(path), ".json")
	if seen[target] {
		return nil, fmt.Errorf("two reports are named %s", target)
	}
	seen[target] = true
	var found []finding
	err := readFile(path, func(r io.Reader) (e error) { found, e = readReport(r, target, im); return e })
	return found, err
}
