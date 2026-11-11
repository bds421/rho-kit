// Command kit-basepin holds a release to current base images. For every
// image a Dockerfile pins (FROM name:tag@sha256:…, directly or through an
// ARG default), every service image a -compose file pins and every
// name:tag@sha256:… a -text file names (a Makefile, a script, a workflow),
// it asks the registry which digest the tag names now. A pin behind its
// tag fails once the tag's digest is past the dependency wait (-wait, 7
// days): the image was rebuilt, usually with package security updates, and
// the release would ship the old one. Within the wait the old pin passes,
// unless -advisories or -fixes names the pinned digest with an advisory,
// which overrides the wait as a security fix does. A tag digest whose push
// time no source tells fails, a build date alone included. A pin of one
// platform's manifest is current while the tag's index lists it. An
// -advisories entry for a digest nothing pins any more is refused, so the
// file holds only what is open; -fixes is generated (a scan of the pinned
// and the tag's newest image, say) and may name digests nothing pins.
//
// -write moves every pin that must move to the tag's digest, in the line
// that names it, and drops the -advisories entries of the digests it
// moved; a pin within its wait stays.
//
// It exits 1 when a pin must move (never with -write), 2 on bad input or
// a registry error.
//
//	kit-basepin [-wait 168h] [-advisories file] [-fixes file] [-write] [-compose file]... [-text file]... [-own prefix]... [Dockerfile]...
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

const usage = "usage: kit-basepin [-wait 168h] [-advisories file] [-fixes file] [-write] [-compose file]... [-text file]... [-own prefix]... [Dockerfile]..."

// inputs are the files the pins and advisories come from.
type inputs struct {
	dockerfiles, compose, text, own list
	advisories, fixes               string
}

func run(args []string, stdout, stderr io.Writer, reg *registry, now time.Time) int {
	flags := flag.NewFlagSet("kit-basepin", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var in inputs
	wait := flags.Duration("wait", 7*24*time.Hour, "how old the tag's digest must be before a pin behind it fails")
	flags.StringVar(&in.advisories, "advisories", "", "JSON list of pinned digests with an advisory (image, digest, advisory, reason)")
	flags.StringVar(&in.fixes, "fixes", "", "a generated list like -advisories; it may name digests nothing pins")
	write := flags.Bool("write", false, "move every pin that must move to the tag's digest")
	flags.Var(&in.compose, "compose", "a compose file whose service images are checked (repeatable)")
	flags.Var(&in.text, "text", "a file whose every name:tag@sha256:… is checked (repeatable)")
	flags.Var(&in.own, "own", "a repository prefix of images built here, skipped in compose and text files (repeatable)")
	if err := flags.Parse(args); err != nil || flags.NArg()+len(in.compose)+len(in.text) == 0 || *wait < 0 {
		_, _ = fmt.Fprintln(stderr, usage)
		return 2
	}
	in.dockerfiles = flags.Args()
	pins, advisories, fixes, err := load(in)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 2
	}
	var moves []move
	failed := false
	for _, p := range pins {
		if p.Unchecked != "" {
			_, _ = fmt.Fprintf(stdout, "%s  %s  not checked: set at run time or without a tag\n", p.Line, p.Unchecked)
			continue
		}
		adv := advisoryFor(advisories, p)
		if adv == nil {
			adv = advisoryFor(fixes, p)
		}
		state, digest, late, err := judge(reg, p, adv, *wait, now)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "%s: %v\n", p.Line, err)
			failed = true
			continue
		}
		if late {
			moves = append(moves, move{pin: p, to: digest})
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
	case *write:
		if err := apply(moves, in.advisories, stdout); err != nil {
			_, _ = fmt.Fprintln(stderr, err)
			return 2
		}
	case len(moves) > 0:
		_, _ = fmt.Fprintf(stdout, "%d pinned images behind their tags\n", len(moves))
		return 1
	}
	return 0
}

func load(in inputs) (pins []pin, advisories, fixes []*advisory, err error) {
	read := func(files []string, parse func(io.Reader, string) ([]pin, error)) {
		for _, file := range files {
			if err != nil {
				return
			}
			var found []pin
			found, err = readFile(file, func(r io.Reader) ([]pin, error) { return parse(r, file) })
			pins = append(pins, found...)
		}
	}
	read(in.dockerfiles, readDockerfile)
	read(in.compose, func(r io.Reader, file string) ([]pin, error) { return readCompose(r, file, in.own) })
	read(in.text, func(r io.Reader, file string) ([]pin, error) { return readText(r, file, in.own) })
	if err == nil && in.advisories != "" {
		advisories, err = readFile(in.advisories, readAdvisories)
	}
	if err == nil && in.fixes != "" {
		fixes, err = readFile(in.fixes, readAdvisories)
	}
	return pins, advisories, fixes, err
}

// judge is the pin's state and the digest its tag names now; late when
// the pin must move to it.
func judge(reg *registry, p pin, adv *advisory, wait time.Duration, now time.Time) (string, string, bool, error) {
	digest, err := reg.current(p)
	if err != nil {
		return "", "", false, err
	}
	if digest != p.Digest && slices.Contains(reg.platforms(p, digest), p.Digest) {
		digest = p.Digest
	}
	if digest == p.Digest {
		if adv != nil {
			return "", "", false, fmt.Errorf("advisory %s names the digest the tag still has; the fix is not in a newer image yet", adv.Advisory)
		}
		return "current", digest, false, nil
	}
	moved := "the tag is " + digest[:19]
	if adv != nil {
		return "BEHIND: " + moved + ", " + adv.Advisory + " (" + adv.Reason + ") overrides the wait", digest, true, nil
	}
	pushed, built := reg.pushed(p, digest)
	switch {
	case pushed.IsZero():
		return "BEHIND: " + moved + ", pushed at an unknown time", digest, true, nil
	case built:
		return "BEHIND: " + moved + ", built " + pushed.UTC().Format(time.DateOnly) + " (a build date: the push time is unknown)", digest, true, nil
	}
	since := " since " + pushed.UTC().Format(time.DateOnly)
	if now.Sub(pushed) >= wait {
		return "BEHIND: " + moved + since, digest, true, nil
	}
	return "newer digest in its wait: " + moved + since + ", due " + pushed.Add(wait).UTC().Format(time.DateOnly), digest, false, nil
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
