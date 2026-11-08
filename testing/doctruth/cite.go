package doctruth

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// citedPath is a path in backticks that may start at a top-level folder.
var citedPath = regexp.MustCompile("`([A-Za-z0-9_.-]+/[A-Za-z0-9_./-]*)`")

// placeholder is a pattern, not a path: a tag `module/vX.Y.Z`.
var placeholder = regexp.MustCompile(`X\.Y\.Z|<|\{`)

func defaultDocuments(f string) bool {
	return f == "docs/ARCHITECTURE.md" || f == "docs/SECURITY.md" || f == "README.md" ||
		strings.HasSuffix(f, "/README.md") || strings.HasPrefix(f, "docs/adr/") ||
		(strings.HasPrefix(f, "deploy/") && strings.Count(f, "/") == 1)
}

// documents are the tracked Markdown files o.Documents chooses.
func documents(o Options) ([]string, error) {
	out, err := exec.Command("git", "-C", o.Root, "ls-files", "--", "*.md").Output() // #nosec G204 -- git over the repository root
	if err != nil {
		return nil, fmt.Errorf("listing the documents: %w", err)
	}
	var files []string
	for _, f := range strings.Fields(string(out)) {
		if o.Documents(f) {
			files = append(files, f)
		}
	}
	return files, nil
}

// topFolders are the repository's top-level folders a cited path starts at.
func topFolders(root string) (map[string]bool, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	tops := map[string]bool{}
	for _, e := range entries {
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
			tops[e.Name()] = true
		}
	}
	return tops, nil
}

// ignored is a path git ignores: a build output the documents may name.
func ignored(root, path string) bool {
	return exec.Command("git", "-C", root, "check-ignore", "-q", path).Run() == nil // #nosec G204 -- git over the repository root
}

func unresolvedCitations(o Options) ([]string, error) {
	docs, err := documents(o)
	if err != nil {
		return nil, err
	}
	tops, err := topFolders(o.Root)
	if err != nil {
		return nil, err
	}
	var findings []string
	for _, doc := range docs {
		f, err := unresolvedIn(o, doc, tops)
		if err != nil {
			return nil, err
		}
		findings = append(findings, f...)
	}
	return findings, nil
}

// elsewhere tells whether a line cites another repository's files; a
// name may break across the line before.
func (o Options) elsewhere(section, previous, line string) bool {
	return o.OtherRepositories != nil && o.OtherRepositories.MatchString(section+"\n"+previous+" "+strings.TrimLeft(line, " \t"))
}

func unresolvedIn(o Options, doc string, tops map[string]bool) ([]string, error) {
	f, err := os.Open(filepath.Join(o.Root, doc)) // #nosec G304 -- a tracked document of the repository
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	var findings []string
	s := bufio.NewScanner(f)
	section, previous, n := "", "", 0
	for s.Scan() {
		n++
		line := s.Text()
		if strings.HasPrefix(line, "#") || strings.HasPrefix(line, "**") {
			section = line
		}
		if !o.elsewhere(section, previous, line) {
			for _, m := range citedPath.FindAllStringSubmatch(line, -1) {
				p := strings.TrimSuffix(m[1], "/")
				if tops[strings.SplitN(p, "/", 2)[0]] && !placeholder.MatchString(p) && !exists(o.Root, p) && !ignored(o.Root, p) {
					findings = append(findings, fmt.Sprintf("%s:%d cites %s, which does not exist", doc, n, p))
				}
			}
		}
		previous = line
	}
	return findings, s.Err()
}
