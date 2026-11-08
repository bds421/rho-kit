package doctruth

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// packageSection is the first of the sections the architecture document
// has, up to the next level-2 heading.
func packageSection(o Options) (string, error) {
	b, err := os.ReadFile(filepath.Join(o.Root, o.Architecture)) // #nosec G304 -- the repository's own document
	if err != nil {
		return "", err
	}
	text := "\n" + string(b)
	for _, heading := range o.Sections {
		start := strings.Index(text, "\n## "+heading+"\n")
		if start < 0 {
			continue
		}
		rest := text[start+1:]
		if end := strings.Index(rest[3:], "\n## "); end >= 0 {
			rest = rest[:end+3]
		}
		return rest, nil
	}
	return "", fmt.Errorf("%s has none of the sections %q", o.Architecture, o.Sections)
}

func exists(root, path string) bool {
	_, err := os.Stat(filepath.Join(root, path))
	return err == nil
}

// missingFolders names each folder the section names that does not exist.
func missingFolders(root, section string) []string {
	var findings []string
	for _, p := range append(treePaths(section), tablePaths(section)...) {
		if !exists(root, p[0]) && (p[1] == "" || !exists(root, p[1])) {
			findings = append(findings, fmt.Sprintf("the package section names %s, which does not exist", p[0]))
		}
	}
	return findings
}

// unnamedPackages names each package under the package roots whose name
// the section does not use as a word.
func unnamedPackages(o Options, section string) ([]string, error) {
	var findings []string
	for _, top := range o.PackageRoots {
		err := filepath.WalkDir(filepath.Join(o.Root, top), func(path string, d fs.DirEntry, err error) error {
			switch {
			case err != nil:
				return err
			case !d.IsDir():
				return nil
			case d.Name() == "testdata" || strings.HasPrefix(d.Name(), "."):
				return filepath.SkipDir
			}
			goFiles, _ := filepath.Glob(filepath.Join(path, "*.go"))
			if len(goFiles) == 0 || o.Unnamed.MatchString(d.Name()) || named(section, d.Name()) {
				return nil
			}
			rel, _ := filepath.Rel(o.Root, path)
			findings = append(findings, fmt.Sprintf("the package section does not name the package %s", filepath.ToSlash(rel)))
			return nil
		})
		if err != nil && !os.IsNotExist(err) {
			return nil, err
		}
	}
	return findings, nil
}

func named(section, name string) bool {
	return regexp.MustCompile(`(^|[^A-Za-z0-9_])` + regexp.QuoteMeta(name) + `($|[^A-Za-z0-9_])`).MatchString(section)
}
