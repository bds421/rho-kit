package main

import (
	"bufio"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"
)

// pin is a base image a Dockerfile or compose file names by tag and digest.
type pin struct {
	Line       string
	Host       string // registry host, "docker.io" for Docker Hub
	Repository string // "library/alpine", "distroless/static-debian13"
	Tag        string
	Digest     string
	// Unchecked holds the raw reference of a compose image set at run time;
	// it is listed, not checked.
	Unchecked string
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
			words := argWords(strings.Join(fields[1:], " "))
			if len(words) != 1 {
				return fmt.Errorf("%s: ARG with more than one name; write one ARG per line", at)
			}
			if name, value, ok := strings.Cut(words[0], "="); ok {
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
// stack is run (the product's own image) and is listed unchecked, so a
// third-party image written that way stays visible. An image of one of
// own (a repository prefix) is built here and checked by its own release.
func readCompose(r io.Reader, file string, own []string) ([]pin, error) {
	var pins []pin
	err := eachLine(r, file, func(at string, fields []string) error {
		if fields[0] != "image:" {
			return nil
		}
		ref := strings.Trim(strings.Join(fields[1:], " "), `"'`)
		if m := composeVar.FindStringSubmatch(ref); m != nil {
			if m[1] == "" {
				pins = append(pins, pin{Line: at, Unchecked: ref})
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

// argWords splits an ARG line's declarations at spaces outside quotes,
// so a quoted value with spaces stays one declaration.
func argWords(s string) []string {
	var words []string
	var word strings.Builder
	var quote rune
	for _, c := range s {
		switch {
		case quote != 0 && c == quote:
			quote = 0
		case quote == 0 && (c == '"' || c == '\''):
			quote = c
		case quote == 0 && c == ' ':
			if word.Len() > 0 {
				words = append(words, word.String())
				word.Reset()
			}
			continue
		}
		word.WriteRune(c)
	}
	if word.Len() > 0 {
		words = append(words, word.String())
	}
	return words
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

// textRef is a pinned reference anywhere on a line: a Makefile variable,
// a script's docker run, a workflow's image.
var textRef = regexp.MustCompile(`[^\s"'=(,]+@sha256:[0-9a-f]{64}`)

// readText returns every name:tag@sha256:… a file names outside comments,
// except images of own; one without its tag is listed, not checked.
func readText(r io.Reader, file string, own []string) ([]pin, error) {
	var pins []pin
	scan := bufio.NewScanner(r)
	for n := 1; scan.Scan(); n++ {
		line := strings.TrimSpace(scan.Text())
		if strings.HasPrefix(line, "#") || strings.HasPrefix(line, "//") {
			continue
		}
		for _, ref := range textRef.FindAllString(line, -1) {
			if slices.ContainsFunc(own, func(prefix string) bool { return strings.HasPrefix(ref, prefix) }) {
				continue
			}
			at := fmt.Sprintf("%s:%d", file, n)
			if p, ok := parsePin(ref); ok {
				p.Line = at
				pins = append(pins, p)
			} else {
				pins = append(pins, pin{Line: at, Unchecked: ref})
			}
		}
	}
	return pins, scan.Err()
}
