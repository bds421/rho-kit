// Command kit-basepin holds a release to current base images. For every
// image a Dockerfile pins (FROM name:tag@sha256:…, or an ARG default of
// that form), it asks the registry which digest the tag names now. A pin
// behind its tag fails once the tag's digest is past the dependency wait
// (-wait, 7 days): the base was rebuilt, usually with package security
// updates, and the release would ship the old one. Within the wait the
// old pin passes, unless -advisories names the pinned digest with an
// advisory, which overrides the wait as a security fix does. A tag digest
// whose push time no source tells fails like a past one. An advisory
// entry for a digest no Dockerfile pins any more is refused, so the file
// holds only what is open.
//
// It exits 1 when a pin must move, 2 on bad input or a registry error.
//
//	kit-basepin [-wait 168h] [-advisories file] Dockerfile...
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"time"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, newRegistry(), time.Now()))
}

func run(args []string, stdout, stderr io.Writer, reg *registry, now time.Time) int {
	flags := flag.NewFlagSet("kit-basepin", flag.ContinueOnError)
	flags.SetOutput(stderr)
	wait := flags.Duration("wait", 7*24*time.Hour, "how old the tag's digest must be before a pin behind it fails")
	advisoriesFile := flags.String("advisories", "", "JSON list of pinned digests with an advisory (image, digest, advisory, reason)")
	if err := flags.Parse(args); err != nil || flags.NArg() == 0 || *wait < 0 {
		_, _ = fmt.Fprintln(stderr, "usage: kit-basepin [-wait 168h] [-advisories file] Dockerfile...")
		return 2
	}
	var advisories []*advisory
	if *advisoriesFile != "" {
		var err error
		if advisories, err = readFile(*advisoriesFile, readAdvisories); err != nil {
			_, _ = fmt.Fprintln(stderr, err)
			return 2
		}
	}
	var pins []pin
	for _, file := range flags.Args() {
		found, err := readFile(file, func(r io.Reader) ([]pin, error) { return readPins(r, file) })
		if err != nil {
			_, _ = fmt.Fprintln(stderr, err)
			return 2
		}
		pins = append(pins, found...)
	}
	behind, failed := 0, false
	for _, p := range pins {
		state, late, err := judge(reg, p, advisoryFor(advisories, p), *wait, now)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "%s: %v\n", p.Line, err)
			failed = true
			continue
		}
		if late {
			behind++
		}
		_, _ = fmt.Fprintf(stdout, "%s  %s@%s  %s\n", p.Line, p.name(), p.Digest[:19], state)
	}
	for _, a := range advisories {
		if !a.used {
			_, _ = fmt.Fprintf(stderr, "advisory %s for %s@%s: no Dockerfile pins that digest any more; remove the entry\n", a.Advisory, a.Image, a.Digest)
			failed = true
		}
	}
	switch {
	case failed:
		return 2
	case behind > 0:
		_, _ = fmt.Fprintf(stdout, "%d pinned base images behind their tags\n", behind)
		return 1
	}
	return 0
}

func judge(reg *registry, p pin, adv *advisory, wait time.Duration, now time.Time) (string, bool, error) {
	digest, err := reg.current(p)
	if err != nil {
		return "", false, err
	}
	if digest == p.Digest {
		if adv != nil {
			return "", false, fmt.Errorf("advisory %s names the digest the tag still has; the fix is not in a newer base yet", adv.Advisory)
		}
		return "current", false, nil
	}
	moved := "the tag is " + digest[:19]
	if adv != nil {
		return "BEHIND: " + moved + ", " + adv.Advisory + " (" + adv.Reason + ") overrides the wait", true, nil
	}
	pushed := reg.pushed(p, digest)
	if pushed.IsZero() {
		return "BEHIND: " + moved + ", pushed at an unknown time", true, nil
	}
	since := " since " + pushed.UTC().Format(time.DateOnly)
	if now.Sub(pushed) >= wait {
		return "BEHIND: " + moved + since, true, nil
	}
	return "newer digest in its wait: " + moved + since + ", due " + pushed.Add(wait).UTC().Format(time.DateOnly), false, nil
}

func readFile[T any](path string, read func(io.Reader) (T, error)) (T, error) {
	f, err := os.Open(path) //nolint:gosec // the caller names the file
	if err != nil {
		var zero T
		return zero, err
	}
	defer func() { _ = f.Close() }()
	return read(f)
}
