package pgtemplate

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"io/fs"
	"runtime/debug"
	"strconv"
)

// Set is one migration set: a name, such as its version table, and its
// files.
type Set struct {
	Name  string
	Files fs.FS
}

// Hash names the state a template's build leaves: every file of every
// set, the versions of modules whose code migrates beside the files (a
// migration runner, a job queue's own migrations), and extra, such as the
// privileges the build grants. Name only modules every test
// binary links through the caller's package; a module others link but one
// binary does not would give that binary a template of its own. Under
// go.work, or replaced by a local directory, a module's version reads
// "(devel)" or none, so a local edit of its migrations does not change
// the hash.
func Hash(sets []Set, modules []string, extra ...string) (string, error) {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "", fmt.Errorf("pgtemplate: the binary carries no build info")
	}
	return sum(sets, modules, info.Deps, extra)
}

func sum(sets []Set, modules []string, deps []*debug.Module, extra []string) (string, error) {
	h := sha256.New()
	write(h, strconv.Itoa(len(sets)))
	for _, s := range sets {
		write(h, s.Name)
		var files []string
		err := fs.WalkDir(s.Files, ".", func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			body, err := fs.ReadFile(s.Files, path)
			if err != nil {
				return err
			}
			files = append(files, path, string(body))
			return nil
		})
		if err != nil {
			return "", fmt.Errorf("pgtemplate: read set %s: %w", s.Name, err)
		}
		write(h, strconv.Itoa(len(files)))
		for _, f := range files {
			write(h, f)
		}
	}
	write(h, strconv.Itoa(len(modules)))
	for _, m := range modules {
		version, err := versionOf(deps, m)
		if err != nil {
			return "", err
		}
		write(h, m)
		write(h, version)
	}
	write(h, strconv.Itoa(len(extra)))
	for _, e := range extra {
		write(h, e)
	}
	return hex.EncodeToString(h.Sum(nil))[:16], nil
}

func versionOf(deps []*debug.Module, path string) (string, error) {
	for _, d := range deps {
		if d.Path != path {
			continue
		}
		if d.Replace != nil {
			return d.Replace.Path + " " + d.Replace.Version, nil
		}
		return d.Version, nil
	}
	return "", fmt.Errorf("pgtemplate: module %s is not linked into this binary", path)
}

// write length-prefixes s, so no two sequences of parts sum alike.
func write(h hash.Hash, s string) {
	_, _ = fmt.Fprintf(h, "%d:%s", len(s), s)
}
