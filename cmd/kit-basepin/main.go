// Command kit-basepin holds a release to current base images. For every
// image a Dockerfile pins (FROM name:tag@sha256:…, directly or through an
// ARG default) and every service image a -compose file pins, it asks the
// registry which digest the tag names now. A pin behind its tag fails once
// the tag's digest is past the dependency wait (-wait, 7 days): the image
// was rebuilt, usually with package security updates, and the release
// would ship the old one. Within the wait the old pin passes, unless
// -advisories names the pinned digest with an advisory, which overrides
// the wait as a security fix does. A tag digest whose push time no source
// tells fails, a build date alone included. A pin of one platform's
// manifest is current while the tag's index lists it. An advisory entry
// for a digest nothing pins any more is refused, so the file holds only
// what is open.
//
// It exits 1 when a pin must move, 2 on bad input or a registry error.
//
//	kit-basepin [-wait 168h] [-advisories file] [-compose file]... [-own prefix]... [Dockerfile]...
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, newRegistry(), time.Now()))
}

type list []string

func (l *list) String() string     { return strings.Join(*l, ",") }
func (l *list) Set(v string) error { *l = append(*l, v); return nil }

const usage = "usage: kit-basepin [-wait 168h] [-advisories file] [-compose file]... [-own prefix]... [Dockerfile]..."

func run(args []string, stdout, stderr io.Writer, reg *registry, now time.Time) int {
	flags := flag.NewFlagSet("kit-basepin", flag.ContinueOnError)
	flags.SetOutput(stderr)
	wait := flags.Duration("wait", 7*24*time.Hour, "how old the tag's digest must be before a pin behind it fails")
	advisoriesFile := flags.String("advisories", "", "JSON list of pinned digests with an advisory (image, digest, advisory, reason)")
	var composeFiles, own list
	flags.Var(&composeFiles, "compose", "a compose file whose service images are checked (repeatable)")
	flags.Var(&own, "own", "a repository prefix of images built here, skipped in compose files (repeatable)")
	if err := flags.Parse(args); err != nil || flags.NArg()+len(composeFiles) == 0 || *wait < 0 {
		_, _ = fmt.Fprintln(stderr, usage)
		return 2
	}
	pins, advisories, err := load(flags.Args(), composeFiles, own, *advisoriesFile)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 2
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
			_, _ = fmt.Fprintf(stderr, "advisory %s for %s@%s: nothing pins that digest any more; remove the entry\n", a.Advisory, a.Image, a.Digest)
			failed = true
		}
	}
	switch {
	case failed:
		return 2
	case behind > 0:
		_, _ = fmt.Fprintf(stdout, "%d pinned images behind their tags\n", behind)
		return 1
	}
	return 0
}

func load(dockerfiles, composeFiles, own []string, advisoriesFile string) ([]pin, []*advisory, error) {
	var pins []pin
	for _, file := range dockerfiles {
		found, err := readFile(file, func(r io.Reader) ([]pin, error) { return readDockerfile(r, file) })
		if err != nil {
			return nil, nil, err
		}
		pins = append(pins, found...)
	}
	for _, file := range composeFiles {
		found, err := readFile(file, func(r io.Reader) ([]pin, error) { return readCompose(r, file, own) })
		if err != nil {
			return nil, nil, err
		}
		pins = append(pins, found...)
	}
	if advisoriesFile == "" {
		return pins, nil, nil
	}
	advisories, err := readFile(advisoriesFile, readAdvisories)
	return pins, advisories, err
}

func judge(reg *registry, p pin, adv *advisory, wait time.Duration, now time.Time) (string, bool, error) {
	digest, err := reg.current(p)
	if err != nil {
		return "", false, err
	}
	if digest != p.Digest && slices.Contains(reg.platforms(p, digest), p.Digest) {
		digest = p.Digest
	}
	if digest == p.Digest {
		if adv != nil {
			return "", false, fmt.Errorf("advisory %s names the digest the tag still has; the fix is not in a newer image yet", adv.Advisory)
		}
		return "current", false, nil
	}
	moved := "the tag is " + digest[:19]
	if adv != nil {
		return "BEHIND: " + moved + ", " + adv.Advisory + " (" + adv.Reason + ") overrides the wait", true, nil
	}
	pushed, built := reg.pushed(p, digest)
	switch {
	case pushed.IsZero():
		return "BEHIND: " + moved + ", pushed at an unknown time", true, nil
	case built:
		return "BEHIND: " + moved + ", built " + pushed.UTC().Format(time.DateOnly) + " (a build date: the push time is unknown)", true, nil
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
