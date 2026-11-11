package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

// move is a pin that must move to its tag's digest: why, and what
// -verify said of the new digest.
type move struct {
	pin      pin
	to       string
	why      string
	verified string
}

// apply replaces each moved digest everywhere in the files read, as a
// digest names one image wherever it is written (a Makefile variable
// built from the tag, a variable's default), and drops the advisories
// (the committed file) of the digests it moved: the fix is in.
func apply(moves []move, files []string, advisoriesFile string, w io.Writer) error {
	if len(moves) == 0 {
		return nil
	}
	pairs := make([]string, 0, 2*len(moves))
	for _, m := range moves {
		pairs = append(pairs, m.pin.Digest, m.to)
	}
	replace := strings.NewReplacer(pairs...)
	for _, file := range files {
		if err := rewrite(file, replace); err != nil {
			return err
		}
	}
	for _, m := range moves {
		note := m.why
		if m.verified != "" {
			note += "; verified: " + m.verified
		}
		_, _ = fmt.Fprintf(w, "%s  %s@%s  moved to %s (%s)\n", m.pin.Line, m.pin.name(), m.pin.Digest[:19], m.to[:19], note)
	}
	if advisoriesFile == "" {
		return nil
	}
	return dropMoved(advisoriesFile, moves)
}

func rewrite(file string, replace *strings.Replacer) error {
	body, err := os.ReadFile(file) //nolint:gosec // a file the caller named
	if err != nil {
		return err
	}
	moved := replace.Replace(string(body))
	if moved == string(body) {
		return nil
	}
	info, err := os.Stat(file)
	if err != nil {
		return err
	}
	return os.WriteFile(file, []byte(moved), info.Mode().Perm())
}

func dropMoved(file string, moves []move) error {
	list, err := readFile(file, readAdvisories)
	if err != nil {
		return err
	}
	kept := []*advisory{}
	for _, a := range list {
		if !movedFrom(moves, a) {
			kept = append(kept, a)
		}
	}
	if len(kept) == len(list) {
		return nil
	}
	body, err := json.MarshalIndent(kept, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(file, append(body, '\n'), 0o644) //nolint:gosec // a committed file, not a secret
}

func movedFrom(moves []move, a *advisory) bool {
	for _, m := range moves {
		if a.name == m.pin.Host+"/"+m.pin.Repository && a.Digest == m.pin.Digest {
			return true
		}
	}
	return false
}

// verifyMoves runs the -verify program for each move's new digest, once
// per image, before anything is written; a failure stops the run.
func verifyMoves(program string, moves []move) error {
	if program == "" {
		return nil
	}
	said := map[string]string{}
	for i, m := range moves {
		image := m.pin.Host + "/" + m.pin.Repository
		key := image + "@" + m.to
		if _, done := said[key]; !done {
			out, err := exec.Command(program, image, m.pin.Tag, m.to).CombinedOutput() //nolint:gosec // the caller names the program
			first, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
			if err != nil {
				return fmt.Errorf("%s: %s:%s@%s failed the check: %s", m.pin.Line, image, m.pin.Tag, m.to[:19], strings.TrimSpace(string(out)))
			}
			said[key] = first
		}
		moves[i].verified = said[key]
	}
	return nil
}
