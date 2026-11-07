package main

import (
	"bufio"
	"fmt"
	"io"
	"regexp"
	"strings"
)

// pin is a base image a Dockerfile or compose file names by tag and digest.
type pin struct {
	Line       string
	Host       string // registry host, "docker.io" for Docker Hub
	Repository string // "library/alpine", "distroless/static-debian13"
	Tag        string
	Digest     string
}

func (p pin) name() string { return p.Host + "/" + p.Repository + ":" + p.Tag }

var (
	pinRef = regexp.MustCompile(`^([^\s@$]+):([A-Za-z0-9_][A-Za-z0-9_.-]{0,127})@(sha256:[0-9a-f]{64})$`)
	// argRef is $X or ${X}, alone.
	argRef = regexp.MustCompile(`^\$\{?([A-Za-z_][A-Za-z0-9_]*)\}?$`)
	// composeVar is ${X...}: ${X:-default} or ${X-default} gives a default,
	// ${X}, ${X:?message} and ${X?message} none.
	composeVar = regexp.MustCompile(`^\$\{[A-Za-z_][A-Za-z0-9_]*(?::?-(.*)|:?\?.*)?\}$`)
)

// readDockerfile returns the FROM images, directly or through an ARG
// whose default is the pinned reference. A FROM that is not pinned, or
// names an ARG whose default is not, is refused: it would build from
// whatever the tag is that day.
func readDockerfile(r io.Reader, file string) ([]pin, error) {
	var pins []pin
	stages := map[string]bool{}
	defaults := map[string]string{}
	err := eachLine(r, file, func(at string, fields []string) error {
		switch strings.ToUpper(fields[0]) {
		case "ARG":
			if name, value, ok := strings.Cut(fields[1], "="); ok {
				defaults[name] = strings.Trim(value, `"'`)
			}
		case "FROM":
			args := fields[1:]
			for len(args) > 0 && strings.HasPrefix(args[0], "--") {
				args = args[1:]
			}
			if len(args) == 0 {
				return nil
			}
			if len(args) >= 3 && strings.EqualFold(args[1], "AS") {
				stages[strings.ToLower(args[2])] = true
			}
			ref := args[0]
			if stages[strings.ToLower(ref)] || ref == "scratch" {
				return nil
			}
			if m := argRef.FindStringSubmatch(ref); m != nil {
				def, ok := defaults[m[1]]
				if !ok {
					return fmt.Errorf("%s: FROM %s: ARG %s has no default to check", at, ref, m[1])
				}
				ref = def
			}
			return add(&pins, at, ref)
		}
		return nil
	})
	return pins, err
}

// readCompose returns the images of a compose file's services. A variable
// with a default is checked by its default; one without is set where the
// stack is run (the product's own image). An image of one of own (a
// repository prefix) is built here and checked by its own release.
func readCompose(r io.Reader, file string, own []string) ([]pin, error) {
	var pins []pin
	err := eachLine(r, file, func(at string, fields []string) error {
		if fields[0] != "image:" {
			return nil
		}
		ref := strings.Trim(strings.Join(fields[1:], " "), `"'`)
		if m := composeVar.FindStringSubmatch(ref); m != nil {
			if m[1] == "" {
				return nil
			}
			ref = m[1]
		}
		for _, prefix := range own {
			if strings.HasPrefix(ref, prefix) {
				return nil
			}
		}
		return add(&pins, at, ref)
	})
	return pins, err
}

func add(pins *[]pin, at, ref string) error {
	p, ok := parsePin(ref)
	if !ok {
		return fmt.Errorf("%s: %s is not pinned as name:tag@sha256:<digest>", at, ref)
	}
	p.Line = at
	*pins = append(*pins, p)
	return nil
}

func eachLine(r io.Reader, file string, fn func(at string, fields []string) error) error {
	scan := bufio.NewScanner(r)
	for n := 1; scan.Scan(); n++ {
		fields := strings.Fields(scan.Text())
		if len(fields) < 2 || strings.HasPrefix(fields[0], "#") {
			continue
		}
		if err := fn(fmt.Sprintf("%s:%d", file, n), fields); err != nil {
			return err
		}
	}
	return scan.Err()
}

func parsePin(ref string) (pin, bool) {
	m := pinRef.FindStringSubmatch(ref)
	if m == nil {
		return pin{}, false
	}
	host, repo := "docker.io", m[1]
	if first, rest, ok := strings.Cut(m[1], "/"); ok && (strings.ContainsAny(first, ".:") || first == "localhost") {
		host, repo = first, rest
	}
	if host == "docker.io" && !strings.Contains(repo, "/") {
		repo = "library/" + repo
	}
	return pin{Host: host, Repository: repo, Tag: m[2], Digest: m[3]}, true
}
