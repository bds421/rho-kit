package main

import (
	"bufio"
	"fmt"
	"io"
	"regexp"
	"strings"
)

// pin is a base image a Dockerfile names by tag and digest.
type pin struct {
	File, Line string
	Host       string // registry host, "docker.io" for Docker Hub
	Repository string // "library/alpine", "distroless/static-debian13"
	Tag        string
	Digest     string
}

func (p pin) name() string { return p.Host + "/" + p.Repository + ":" + p.Tag }

var pinRef = regexp.MustCompile(`^([^\s@$]+):([A-Za-z0-9_][A-Za-z0-9_.-]{0,127})@(sha256:[0-9a-f]{64})$`)

// readPins returns the FROM images, and ARG defaults that are image
// references, pinned by tag and digest. A FROM without a digest is refused:
// it would build from whatever the tag is that day.
func readPins(r io.Reader, file string) ([]pin, error) {
	var pins []pin
	stages := map[string]bool{}
	scan := bufio.NewScanner(r)
	for n := 1; scan.Scan(); n++ {
		fields := strings.Fields(scan.Text())
		if len(fields) < 2 {
			continue
		}
		at := fmt.Sprintf("%s:%d", file, n)
		switch strings.ToUpper(fields[0]) {
		case "FROM":
			args := fields[1:]
			for len(args) > 0 && strings.HasPrefix(args[0], "--") {
				args = args[1:]
			}
			if len(args) == 0 {
				continue
			}
			if len(args) >= 3 && strings.EqualFold(args[1], "AS") {
				stages[strings.ToLower(args[2])] = true
			}
			ref := args[0]
			if stages[strings.ToLower(ref)] || strings.Contains(ref, "$") || ref == "scratch" {
				continue
			}
			p, ok := parsePin(ref)
			if !ok {
				return nil, fmt.Errorf("%s: %s is not pinned as name:tag@sha256:<digest>", at, ref)
			}
			p.File, p.Line = file, at
			pins = append(pins, p)
		case "ARG":
			if _, ref, ok := strings.Cut(fields[1], "="); ok {
				if p, ok := parsePin(ref); ok {
					p.File, p.Line = file, at
					pins = append(pins, p)
				}
			}
		}
	}
	return pins, scan.Err()
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
