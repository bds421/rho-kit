package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

// move is a pin that must move to its tag's digest.
type move struct {
	pin pin
	to  string
}

// apply writes each move into the line that names the pin and drops the
// advisories (the committed file) of the digests it moved: the fix is in.
func apply(moves []move, advisoriesFile string, w io.Writer) error {
	byFile := map[string][]move{}
	var order []string
	for _, m := range moves {
		file, _, _ := cutLine(m.pin.Line)
		if _, seen := byFile[file]; !seen {
			order = append(order, file)
		}
		byFile[file] = append(byFile[file], m)
	}
	for _, file := range order {
		if err := rewrite(file, byFile[file]); err != nil {
			return err
		}
		for _, m := range byFile[file] {
			_, _ = fmt.Fprintf(w, "%s  %s@%s  moved to %s\n", m.pin.Line, m.pin.name(), m.pin.Digest[:19], m.to[:19])
		}
	}
	if advisoriesFile == "" || len(moves) == 0 {
		return nil
	}
	return dropMoved(advisoriesFile, moves)
}

// cutLine splits file:line.
func cutLine(at string) (string, int, error) {
	i := strings.LastIndex(at, ":")
	n, err := strconv.Atoi(at[i+1:])
	return at[:i], n, err
}

func rewrite(file string, moves []move) error {
	body, err := os.ReadFile(file) //nolint:gosec // a file the caller named
	if err != nil {
		return err
	}
	lines := strings.Split(string(body), "\n")
	for _, m := range moves {
		_, n, err := cutLine(m.pin.Line)
		if err != nil || n < 1 || n > len(lines) || !strings.Contains(lines[n-1], m.pin.Digest) {
			return fmt.Errorf("%s: the pin %s is no longer on that line", m.pin.Line, m.pin.Digest[:19])
		}
		lines[n-1] = strings.ReplaceAll(lines[n-1], m.pin.Digest, m.to)
	}
	info, err := os.Stat(file)
	if err != nil {
		return err
	}
	return os.WriteFile(file, []byte(strings.Join(lines, "\n")), info.Mode().Perm())
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
