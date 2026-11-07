package main

import (
	"errors"
	"path"
	"slices"
)

// component is a third-party artifact that ships but is not built from
// our code: an upstream image (by its repository) or a binary someone
// else releases inside our image (by report name and file path). Each
// field given must match; a finding no component holds is ours.
type component struct {
	Name string `json:"component"`
	// Image is the repository of the scanned image ("postgres",
	// "example.com/team/db"), as a registry scan or image=report.json
	// names it.
	Image string `json:"image,omitempty"`
	// Target is a pattern (path.Match) of the report's name without
	// ".json".
	Target string `json:"target,omitempty"`
	// Path is a pattern (path.Match) of the file the package was found in.
	Path string `json:"path,omitempty"`
}

func (c component) validate() error {
	if c.Name == "" {
		return errors.New("component (its name) is required")
	}
	if c.Image == "" && c.Target == "" && c.Path == "" {
		return errors.New("image, target or path is required")
	}
	for _, pattern := range []string{c.Target, c.Path} {
		if _, err := path.Match(pattern, ""); err != nil {
			return errors.New(pattern + ": " + err.Error())
		}
	}
	return nil
}

func (c component) holds(f finding) bool {
	if c.Image != "" && repository(c.Image) != f.Image.repository {
		return false
	}
	if c.Target != "" && !matches(c.Target, f.Target) {
		return false
	}
	return c.Path == "" || slices.ContainsFunc(f.Paths, func(p string) bool { return matches(c.Path, p) })
}

func matches(pattern, name string) bool {
	ok, _ := path.Match(pattern, name)
	return ok
}
