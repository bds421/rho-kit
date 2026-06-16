// Command release-planner inspects the rho-kit Go workspace and prints the
// per-module release plan for a target version. It walks `go.work`, computes
// which modules have changed since their previous tag (or selects all in
// `-mode=all`), groups them by dependency level, and emits the result as
// text, TSV, or a ready-to-pipe list of git tags. The Makefile's
// `release-plan` target wraps it and is the supported invocation; the
// command is run from outside CI to dry-run a release tagging pass.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

type module struct {
	Dir          string
	Path         string
	Requires     []string
	Deps         []string
	ChangedFiles []string
	RootImpacted bool
	PreviousTag  string
}

func main() {
	repo := flag.String("repo", ".", "repository root")
	version := flag.String("version", "v2.0.0", "release version")
	mode := flag.String("mode", "changed", "release mode: changed or all")
	base := flag.String("base", "HEAD~1", "base ref for changed mode")
	format := flag.String("format", "text", "output format: text, tsv, or tags")
	global := flag.String("global", "none", "global change policy in changed mode: none or all")
	includeWorktree := flag.Bool("include-worktree", true, "include staged and unstaged changes")
	flag.Parse()

	root, err := filepath.Abs(*repo)
	must(err)
	must(os.Chdir(root))

	dirs, err := parseGoWork("go.work")
	must(err)
	if len(dirs) == 0 {
		fatal("go.work does not list any workspace modules")
	}

	modules := make(map[string]*module, len(dirs))
	pathToDir := make(map[string]string, len(dirs))
	for _, dir := range dirs {
		modPath, reqs, err := parseGoMod(filepath.Join(dir, "go.mod"))
		must(err)
		m := &module{Dir: dir, Path: modPath, Requires: reqs}
		modules[dir] = m
		pathToDir[modPath] = dir
	}

	for _, dir := range dirs {
		m := modules[dir]
		for _, req := range m.Requires {
			depDir, ok := pathToDir[req]
			if ok && depDir != dir {
				m.Deps = appendUnique(m.Deps, depDir)
			}
		}
		sort.Strings(m.Deps)
		m.PreviousTag = latestTag(dir)
	}

	changedFiles, warnings := collectChangedFiles(*base, *includeWorktree)
	rootChanges := assignChangedFiles(dirs, modules, changedFiles)

	selected := map[string]bool{}
	if *mode == "all" {
		for _, dir := range dirs {
			selected[dir] = true
		}
	} else if *mode == "changed" {
		for _, dir := range dirs {
			if len(modules[dir].ChangedFiles) > 0 {
				selected[dir] = true
			}
		}
		if *global == "all" && len(rootChanges) > 0 {
			for _, dir := range dirs {
				selected[dir] = true
				modules[dir].RootImpacted = true
			}
		}
		addDependents(selected, dirs, modules)
	} else {
		fatal("unknown -mode %q; use changed or all", *mode)
	}

	levels, err := dependencyLevels(dirs, modules, selected)
	must(err)

	switch *format {
	case "text":
		printText(*version, *mode, *base, *global, dirs, modules, selected, levels, rootChanges, warnings)
	case "tsv":
		printTSV(*version, modules, levels)
	case "tags":
		printTags(*version, modules, levels)
	default:
		fatal("unknown -format %q; use text, tsv, or tags", *format)
	}
}

func parseGoWork(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var dirs []string
	inUse := false
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := stripComment(strings.TrimSpace(scanner.Text()))
		if line == "" {
			continue
		}
		if line == "use (" {
			inUse = true
			continue
		}
		if inUse && line == ")" {
			inUse = false
			continue
		}
		if strings.HasPrefix(line, "use ") {
			dir := strings.TrimSpace(strings.TrimPrefix(line, "use "))
			dirs = append(dirs, cleanModuleDir(dir))
			continue
		}
		if inUse {
			dirs = append(dirs, cleanModuleDir(line))
		}
	}
	sort.Strings(dirs)
	return dirs, scanner.Err()
}

func parseGoMod(path string) (string, []string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", nil, err
	}
	defer f.Close()

	var modPath string
	var requires []string
	inRequire := false
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := stripComment(strings.TrimSpace(scanner.Text()))
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		switch {
		case fields[0] == "module" && len(fields) >= 2:
			modPath = fields[1]
		case fields[0] == "require" && len(fields) >= 2 && fields[1] == "(":
			inRequire = true
		case inRequire && fields[0] == ")":
			inRequire = false
		case inRequire && len(fields) >= 2:
			requires = append(requires, fields[0])
		case fields[0] == "require" && len(fields) >= 3:
			requires = append(requires, fields[1])
		}
	}
	if modPath == "" {
		return "", nil, fmt.Errorf("%s does not declare a module path", path)
	}
	return modPath, requires, scanner.Err()
}

func stripComment(line string) string {
	if i := strings.Index(line, "//"); i >= 0 {
		line = line[:i]
	}
	return strings.TrimSpace(line)
}

func cleanModuleDir(dir string) string {
	dir = strings.Trim(dir, `"`)
	dir = strings.TrimPrefix(dir, "./")
	if dir == "." {
		return "."
	}
	return filepath.ToSlash(filepath.Clean(dir))
}

func collectChangedFiles(base string, includeWorktree bool) ([]string, []string) {
	seen := map[string]bool{}
	var files []string
	var warnings []string
	add := func(lines []string) {
		for _, line := range lines {
			line = filepath.ToSlash(strings.TrimSpace(line))
			if line == "" || seen[line] {
				continue
			}
			seen[line] = true
			files = append(files, line)
		}
	}

	if base != "" {
		if commandOK("git", "rev-parse", "--verify", base+"^{commit}") {
			add(commandLines("git", "diff", "--name-only", base+"..HEAD", "--"))
		} else {
			warnings = append(warnings, fmt.Sprintf("base ref %q does not resolve; committed diff skipped", base))
		}
	}
	if includeWorktree {
		add(commandLines("git", "diff", "--name-only", "--cached", "--"))
		add(commandLines("git", "diff", "--name-only", "--"))
	}
	sort.Strings(files)
	return files, warnings
}

func assignChangedFiles(dirs []string, modules map[string]*module, files []string) []string {
	var rootChanges []string
	for _, file := range files {
		best := ""
		for _, dir := range dirs {
			if file == dir || strings.HasPrefix(file, dir+"/") {
				if len(dir) > len(best) {
					best = dir
				}
			}
		}
		if best == "" {
			rootChanges = append(rootChanges, file)
			continue
		}
		modules[best].ChangedFiles = append(modules[best].ChangedFiles, file)
	}
	return rootChanges
}

func addDependents(selected map[string]bool, dirs []string, modules map[string]*module) {
	changed := true
	for changed {
		changed = false
		for _, dir := range dirs {
			if selected[dir] {
				continue
			}
			for _, dep := range modules[dir].Deps {
				if selected[dep] {
					selected[dir] = true
					changed = true
					break
				}
			}
		}
	}
}

func dependencyLevels(dirs []string, modules map[string]*module, selected map[string]bool) ([][]string, error) {
	remaining := map[string]bool{}
	for _, dir := range dirs {
		if selected[dir] {
			remaining[dir] = true
		}
	}

	var levels [][]string
	for len(remaining) > 0 {
		var level []string
		for _, dir := range dirs {
			if !remaining[dir] {
				continue
			}
			blocked := false
			for _, dep := range modules[dir].Deps {
				if remaining[dep] {
					blocked = true
					break
				}
			}
			if !blocked {
				level = append(level, dir)
			}
		}
		if len(level) == 0 {
			var cycle []string
			for dir := range remaining {
				cycle = append(cycle, dir)
			}
			sort.Strings(cycle)
			return nil, fmt.Errorf("cycle or unresolved dependency among selected modules: %s", strings.Join(cycle, ", "))
		}
		levels = append(levels, level)
		for _, dir := range level {
			delete(remaining, dir)
		}
	}
	return levels, nil
}

func printText(version, mode, base, global string, dirs []string, modules map[string]*module, selected map[string]bool, levels [][]string, rootChanges, warnings []string) {
	fmt.Println("rho-kit module release plan")
	fmt.Printf("version: %s\n", version)
	fmt.Printf("mode: %s\n", mode)
	if mode == "changed" {
		fmt.Printf("base ref: %s\n", base)
		fmt.Printf("global change policy: %s\n", global)
	}
	fmt.Printf("workspace modules: %d\n", len(dirs))
	fmt.Printf("selected modules: %d\n", countSelected(selected))
	for _, warning := range warnings {
		fmt.Printf("warning: %s\n", warning)
	}

	if len(rootChanges) > 0 {
		fmt.Println()
		fmt.Println("changes outside workspace modules:")
		for _, file := range rootChanges {
			fmt.Printf("  %s\n", file)
		}
	}

	fmt.Println()
	fmt.Println("changed workspace modules:")
	anyChanged := false
	for _, dir := range dirs {
		m := modules[dir]
		if len(m.ChangedFiles) == 0 && !m.RootImpacted {
			continue
		}
		anyChanged = true
		fmt.Printf("  %s", dir)
		if m.PreviousTag != "" {
			fmt.Printf(" (previous tag: %s)", m.PreviousTag)
		} else {
			fmt.Print(" (previous tag: none)")
		}
		if m.RootImpacted {
			fmt.Print(" [global-change policy]")
		}
		fmt.Println()
		for _, file := range m.ChangedFiles {
			fmt.Printf("    - %s\n", file)
		}
	}
	if !anyChanged {
		fmt.Println("  none")
	}

	fmt.Println()
	fmt.Println("dependency levels within selected set, selected dependencies first:")
	if len(levels) == 0 {
		fmt.Println("  none")
		return
	}
	for i, level := range levels {
		fmt.Printf("  level %d (%d modules):\n", i, len(level))
		for _, dir := range level {
			m := modules[dir]
			labels := []string{}
			if len(m.ChangedFiles) > 0 {
				labels = append(labels, "changed")
			}
			if m.RootImpacted {
				labels = append(labels, "global")
			}
			if len(labels) == 0 {
				labels = append(labels, "dependent")
			}
			prev := "none"
			if m.PreviousTag != "" {
				prev = m.PreviousTag
			}
			fmt.Printf("    %s -> %s/%s [%s, previous=%s]\n", dir, dir, version, strings.Join(labels, ","), prev)
			if len(m.Deps) > 0 {
				fmt.Printf("      internal deps: %s\n", strings.Join(m.Deps, ", "))
			}
		}
	}
}

func printTSV(version string, modules map[string]*module, levels [][]string) {
	fmt.Println("level\tdir\tmodule\ttag\tchanged\tprevious_tag\tinternal_deps")
	for i, level := range levels {
		for _, dir := range level {
			m := modules[dir]
			changed := len(m.ChangedFiles) > 0 || m.RootImpacted
			fmt.Printf("%d\t%s\t%s\t%s/%s\t%t\t%s\t%s\n",
				i, dir, m.Path, dir, version, changed, m.PreviousTag, strings.Join(m.Deps, ","))
		}
	}
}

func printTags(version string, modules map[string]*module, levels [][]string) {
	for i, level := range levels {
		fmt.Printf("# level %d\n", i)
		for _, dir := range level {
			fmt.Printf("%s/%s\n", modules[dir].Dir, version)
		}
	}
}

func countSelected(selected map[string]bool) int {
	count := 0
	for _, ok := range selected {
		if ok {
			count++
		}
	}
	return count
}

func latestTag(dir string) string {
	lines := commandLines("git", "tag", "--list", dir+"/v[0-9]*", "--sort=-v:refname")
	if len(lines) == 0 {
		return ""
	}
	return strings.TrimSpace(lines[0])
}

func commandOK(name string, args ...string) bool {
	return exec.Command(name, args...).Run() == nil
}

func commandLines(name string, args ...string) []string {
	out, err := exec.Command(name, args...).Output()
	if err != nil {
		return nil
	}
	var lines []string
	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

func appendUnique(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func must(err error) {
	if err != nil {
		fatal("%v", err)
	}
}

func fatal(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, "ERROR: "+format+"\n", args...)
	os.Exit(1)
}
