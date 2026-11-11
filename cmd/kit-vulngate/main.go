// Command kit-vulngate holds a release to the patch deadlines. It reads
// grype JSON reports (grype <target> -o json), one per image or SBOM that
// ships, and lists every vulnerability with its owner and deadline,
// counted from the day its fix was published: CISA KEV 72 hours, critical
// 7 days, high 30, medium, low and without a severity 90; a -policy file
// sets other deadlines and names the third-party components.
//
// It exits 1 when a fixed finding is past its deadline and not accepted:
// in what we own (all that no third-party component holds), or in a
// third-party component, unless the -baseline marks it inherited; 2 on
// bad input, a missing named file included. An inherited finding is
// tracked, not failed, as its deadline counts from when the component
// ships a fix, which the reports cannot tell; a known exploited one fails
// once inheritedKev (14 days) has passed since the package's fix. A
// finding a release adds stays new, held to the due date the baseline
// keeps for it, until it is gone. A vulnerability without a fix is
// listed, never failed.
//
// -record writes this run's third-party findings as the baseline of
// -release, when the gate passes: entries of the previous baseline keep
// their state and due date, any other is new. -previous names the release
// before this one; a baseline of neither is refused.
//
// -report lists what is deployed instead of gating a release, by the
// same rules, for a monitor: it exits 1 when a finding is past its
// deadline. -record needs -baseline, except once: -report -record
// -initial, without -baseline, writes the first baseline with every
// third-party finding inherited. Marking an entry inherited later is an
// edit of the committed file, reviewed.
//
// An acceptance with an image applies only to reports of that image: a
// registry scan by digest names it, and image=report.json names it for a
// report of an archive built from it.
//
// -upstream pinned=report.json names the report of a scan of the pinned
// image's tag as it is now. A finding that would fail in the pinned image
// but that newest image still carries waits on upstream: no release of
// ours can fix it, so it is listed as "waits on upstream: <newest image>
// <id>" and recorded with that image in the baseline, and passes.
//
//	kit-vulngate [-policy file] [-accepted file] [-baseline file [-previous release]] [-record file -release name] [-report [-initial]] [-upstream pinned=report.json]... [image=]report.json...
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

const usage = "usage: kit-vulngate [-policy file] [-accepted file] [-baseline file [-previous release]] [-record file -release name] [-report [-initial]] [-upstream pinned=report.json]... [image=]grype-report.json..."

type list []string

func (l *list) String() string     { return strings.Join(*l, ",") }
func (l *list) Set(v string) error { *l = append(*l, v); return nil }

func run(args []string, stdout, stderr io.Writer, now time.Time) int {
	flags := flag.NewFlagSet("kit-vulngate", flag.ContinueOnError)
	flags.SetOutput(stderr)
	policyFile := flags.String("policy", "", "JSON deadlines and third-party components")
	acceptedFile := flags.String("accepted", "", "JSON list of accepted findings (id, package, image, reason, until)")
	baselineFile := flags.String("baseline", "", "JSON third-party findings of the previous release (as -record writes them)")
	previous := flags.String("previous", "", "the release before this one, which the baseline must be of (or of -release)")
	recordFile := flags.String("record", "", "write this run's third-party findings here, for the next release's -baseline")
	release := flags.String("release", "", "the release -record writes the baseline of")
	reportMode := flags.Bool("report", false, "list what is deployed, by the same rules")
	initial := flags.Bool("initial", false, "with -report -record and no -baseline: write the first baseline, every third-party finding inherited")
	var upstreams list
	flags.Var(&upstreams, "upstream", "pinned=report.json: a scan of the pinned image's tag as it is now (repeatable)")
	if err := flags.Parse(args); err != nil || flags.NArg() == 0 || (*recordFile != "") != (*release != "") {
		_, _ = fmt.Fprintln(stderr, usage)
		return 2
	}
	if err := recordable(*recordFile, *baselineFile, *reportMode, *initial); err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 2
	}
	j, err := load(*policyFile, *acceptedFile, *baselineFile)
	if err == nil {
		j.upstream, err = loadUpstream(upstreams)
	}
	if err == nil {
		err = j.checkBaseline(stdout, *previous, *release)
	}
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 2
	}
	j.now, j.deployed, j.initial = now, *reportMode, *initial
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
		if err := j.record(*recordFile, *release, all); err != nil {
			_, _ = fmt.Fprintln(stderr, err)
			return 2
		}
	}
	if late > 0 {
		return 1
	}
	return 0
}

// recordable refuses -record without -baseline, unless -initial: a
// missing baseline must never turn every third-party finding inherited.
// -initial is only for -report -record without -baseline.
func recordable(record, baseline string, report, initial bool) error {
	if initial && (!report || record == "" || baseline != "") {
		return fmt.Errorf("-initial is only for -report -record without -baseline")
	}
	if record != "" && baseline == "" && !initial {
		return fmt.Errorf("-record needs -baseline (or -report -initial for the first baseline)")
	}
	return nil
}

// checkBaseline names the baseline's release in the output and refuses
// one that is neither of the previous release nor of this one (a rerun).
func (j judge) checkBaseline(w io.Writer, previous, release string) error {
	if j.baseline == nil {
		if previous != "" {
			return fmt.Errorf("-previous %s needs a -baseline", previous)
		}
		return nil
	}
	_, _ = fmt.Fprintf(w, "baseline of release %s, recorded %s\n", j.baseline.Release, j.baseline.Recorded)
	if previous != "" && j.baseline.Release != previous && j.baseline.Release != release {
		return fmt.Errorf("the baseline is of release %s, not of the previous release %s", j.baseline.Release, previous)
	}
	return nil
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
