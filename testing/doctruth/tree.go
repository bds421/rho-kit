package doctruth

import (
	"path"
	"regexp"
	"strings"
)

// nameColumn is where a tree line's description starts: a folder written
// further right is prose.
const nameColumn = 24

// treeLine is one line of a folder tree: its indent and the folders it
// opens with.
type treeLine struct {
	indent  int
	folders []string
}

func readTreeLine(line string) treeLine {
	trimmed := strings.TrimLeft(line, " ")
	l := treeLine{indent: len(line) - len(trimmed)}
	if l.indent >= nameColumn {
		return l
	}
	for _, field := range strings.Fields(trimmed) {
		if !strings.HasSuffix(field, "/") || strings.ContainsAny(field, "<*") {
			break
		}
		l.folders = append(l.folders, field)
	}
	return l
}

// treePaths reads the section's code blocks as indented trees. A folder
// sits below the nearest less indented one; a further folder on the same
// line sits there too or beside the first ("wire/entity/ report/" is
// wire/report/). Each entry is the path and the beside one.
func treePaths(section string) [][2]string {
	var paths [][2]string
	var parents []treeLine
	inBlock := false
	for _, line := range strings.Split(section, "\n") {
		if strings.HasPrefix(line, "```") {
			inBlock, parents = !inBlock, nil
			continue
		}
		l := readTreeLine(line)
		if !inBlock || len(l.folders) == 0 {
			continue
		}
		for len(parents) > 0 && parents[len(parents)-1].indent >= l.indent {
			parents = parents[:len(parents)-1]
		}
		parent := ""
		if len(parents) > 0 {
			parent = parents[len(parents)-1].folders[0]
		}
		first := parent + l.folders[0]
		paths = append(paths, [2]string{strings.TrimSuffix(first, "/"), ""})
		for _, f := range l.folders[1:] {
			beside := path.Dir(strings.TrimSuffix(first, "/")) + "/" + f
			paths = append(paths, [2]string{strings.TrimSuffix(parent+f, "/"), strings.TrimSuffix(beside, "/")})
		}
		parents = append(parents, treeLine{indent: l.indent, folders: []string{first}})
	}
	return paths
}

// tableCell is a backticked entry of a table row's first cell.
var tableCell = regexp.MustCompile("`([^`]+)`")

// tablePaths reads a package table: a row's first cell names a package
// (`internal/a/b`), siblings beside it (`, c` is internal/a/c) and braces
// several at once (`internal/a/{b,c}`). A template cell (`<`, `*`) and a
// row without a closed cell name nothing.
func tablePaths(section string) [][2]string {
	var paths [][2]string
	for _, line := range strings.Split(section, "\n") {
		if !strings.HasPrefix(line, "| `") {
			continue
		}
		cells := tableCell.FindAllStringSubmatch(strings.SplitN(line[1:], "|", 2)[0], -1)
		if len(cells) == 0 || strings.ContainsAny(cells[0][1], "<*") {
			continue
		}
		first := cells[0][1]
		for _, p := range expandBraces(first) {
			paths = append(paths, [2]string{p, ""})
		}
		for _, c := range cells[1:] {
			if strings.ContainsAny(c[1], "<*") {
				continue
			}
			paths = append(paths, [2]string{path.Dir(first) + "/" + c[1], ""})
		}
	}
	return paths
}

func expandBraces(p string) []string {
	open, end := strings.Index(p, "{"), strings.Index(p, "}")
	if open < 0 || end < open {
		return []string{p}
	}
	var out []string
	for _, alt := range strings.Split(p[open+1:end], ",") {
		out = append(out, p[:open]+alt+p[end+1:])
	}
	return out
}
