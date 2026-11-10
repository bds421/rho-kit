// Command check-expand-only keeps the kit's own schema migrations
// expand-only (testing/expandonly): a product runs a kit module's
// migrations beside its own, and rolls back onto the schema they left. A
// set is every folder holding a goose migration; the migrations released
// before the rule are history.
//
// # Exit codes
//
//	0  every set is expand-only
//	1  a finding
//	2  CLI / discovery failure
package main

import (
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"

	"github.com/bds421/rho-kit/testing/expandonly/v2"
)

// releasedBeforeTheRule is each set's newest version when the rule came
// in. A set not named here is checked from its first file.
var releasedBeforeTheRule = map[string]int64{
	"data/actionlog/postgres/migrations":         20260507000001,
	"data/apikey/postgres/migrations":            20260609000001,
	"data/approval/postgres/migrations":          20260720000002,
	"data/cron/pgstore/migrations":               20260601000001,
	"data/idempotency/pgstore/migrations":        20260505000001,
	"data/saga/pgstore/migrations":               20260720000001,
	"infra/outbox/postgres/migrations":           20260722000005,
	"observability/auditlog/postgres/migrations": 20261109000001,
}

var gooseUp = regexp.MustCompile(`(?mi)^--\s*\+goose\s+up\b`)

// skipped are the folders that hold no set of the kit.
var skipped = map[string]bool{".git": true, ".worktrees": true, "testdata": true, "node_modules": true}

func main() {
	root := flag.String("root", ".", "the repository root")
	flag.Parse()
	os.Exit(run(*root, releasedBeforeTheRule, os.Stdout, os.Stderr))
}

func run(root string, history map[string]int64, stdout, stderr io.Writer) int {
	sets, err := discover(root, history)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "check-expand-only:", err)
		return 2
	}
	findings, err := expandonly.Check(expandonly.Options{Sets: sets})
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "check-expand-only:", err)
		return 2
	}
	seen := map[string]bool{}
	for _, s := range sets {
		seen[s.Name] = true
	}
	stale := 0
	for name := range history {
		if !seen[name] {
			_, _ = fmt.Fprintf(stdout, "%s has no migrations: remove it from releasedBeforeTheRule\n", name)
			stale++
		}
	}
	for _, f := range findings {
		_, _ = fmt.Fprintln(stdout, f)
	}
	if stale+len(findings) > 0 {
		return 1
	}
	_, _ = fmt.Fprintf(stdout, "check-expand-only OK (%d sets)\n", len(sets))
	return 0
}

// discover returns every folder under root holding a goose migration.
func discover(root string, history map[string]int64) ([]expandonly.Set, error) {
	dirs := map[string]bool{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		switch {
		case err != nil:
			return err
		case d.IsDir() && skipped[d.Name()]:
			return filepath.SkipDir
		case d.IsDir() || filepath.Ext(path) != ".sql" || dirs[filepath.Dir(path)]:
			return nil
		}
		src, err := os.ReadFile(path) //nolint:gosec // a file of the repository's tree
		if err != nil {
			return err
		}
		if gooseUp.Match(src) {
			dirs[filepath.Dir(path)] = true
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sets := make([]expandonly.Set, 0, len(dirs))
	for dir := range dirs {
		rel, err := filepath.Rel(root, dir)
		if err != nil {
			return nil, err
		}
		rel = filepath.ToSlash(rel)
		sets = append(sets, expandonly.Set{Name: rel, Dir: os.DirFS(dir), After: history[rel]})
	}
	sort.Slice(sets, func(i, j int) bool { return sets[i].Name < sets[j].Name })
	return sets, nil
}
