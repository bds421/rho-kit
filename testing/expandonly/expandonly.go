// Package expandonly keeps a service's schema migrations expand-only, as a
// test. A rollback runs the previous release on the schema the newer one
// migrated, which it never takes back, so every migration must leave the
// previous release working: it adds, it does not remove, rename, retype or
// tighten. A migration that must contract declares it in a comment,
//
//	-- contract: v1.4.0 orders.legacy_ref is unused since v1.4.0
//
// naming the first release that no longer uses what it removes; no
// rollback may then go below that release (the rollback floor). A
// library's sets a product runs beside its own are named Upstream: their
// comments name the library's releases, which the product adopts under its
// own. Statements are classified from PostgreSQL's own parse tree
// (pg_query), DO blocks through their PL/pgSQL. Run it from one test in the
// repository:
//
//	func TestMigrationsAreExpandOnly(t *testing.T) {
//		expandonly.Run(t, expandonly.Options{
//			Sets:  []expandonly.Set{{Name: "orders", Dir: orders.Migrations(), After: 12}},
//			Floor: "../../deploy/rollback-floor",
//		})
//	}
package expandonly

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// Set is one migration set: goose SQL files named <version>_<name>.sql.
type Set struct {
	// Name labels the set in findings.
	Name string
	Dir  fs.FS
	// After is the newest version that is history: migrations up to it were
	// released before the rule held and are not checked.
	After int64
	// Upstream names where the files come from when they are not this
	// repository's (a library's migrations, such as "rho-kit
	// infra/outbox/postgres v2.38.0"). Their contract comments name the
	// library's releases, not this product's: each one past After must be
	// adopted (Options.Adopted) under the product release that first ships
	// it, and that release, not the comment's, counts for the floor.
	Upstream string
}

// Options names the sets and the rollback floor file.
type Options struct {
	Sets []Set
	// Floor is the file holding the rollback floor: the newest version a
	// contract comment names, or nothing when none does. Empty skips the
	// check.
	Floor string
	// Adopted maps an upstream set's contracting file ("<set name>/<file>") to
	// the first release of this product that ships it.
	Adopted map[string]string
}

// Finding is one statement that contracts the schema without a declaration,
// or a declaration or floor that does not hold.
type Finding struct {
	File      string
	Line      int
	Statement string
	Reason    string
	// Upstream is the set's Upstream: the file is not this repository's.
	Upstream string
}

func (f Finding) String() string {
	if f.Statement == "" {
		return fmt.Sprintf("%s: %s", f.File, f.Reason)
	}
	if f.Upstream != "" {
		return fmt.Sprintf("%s:%d: %s %s; the file is %s's: it declares the contraction there (-- contract: vX.Y.Z <why>), then this product adopts it (Options.Adopted)",
			f.File, f.Line, f.Statement, f.Reason, f.Upstream)
	}
	return fmt.Sprintf("%s:%d: %s %s; if the previous release no longer uses it, declare it in the file: -- contract: vX.Y.Z <why>",
		f.File, f.Line, f.Statement, f.Reason)
}

// Run reports each finding of Check as a test error.
func Run(t *testing.T, o Options) {
	t.Helper()
	findings, err := Check(o)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range findings {
		t.Error(f)
	}
}

var (
	versioned = regexp.MustCompile(`^(\d+)_.*\.sql$`)
	contract  = regexp.MustCompile(`(?m)^--[ \t]*contract:[ \t]*(\S*)[ \t]*(.*)$`)
	release   = regexp.MustCompile(`^v(\d+)\.(\d+)\.(\d+)$`)
)

// Check returns the findings of every set and of the floor.
func Check(o Options) ([]Finding, error) {
	var findings []Finding
	floor := ""
	adopted := map[string]bool{}
	for _, s := range o.Sets {
		files, err := fs.Glob(s.Dir, "*.sql")
		if err != nil {
			return nil, err
		}
		for _, name := range files {
			m := versioned.FindStringSubmatch(path.Base(name))
			if m == nil {
				continue
			}
			version, err := strconv.ParseInt(m[1], 10, 64)
			if err != nil {
				return nil, err
			}
			src, err := fs.ReadFile(s.Dir, name)
			if err != nil {
				return nil, err
			}
			label := s.Name + "/" + name
			declared, found := declaration(label, string(src))
			for i := range found {
				if s.Upstream != "" {
					found[i].Reason += " (the file is " + s.Upstream + "'s: fix it there)"
				}
			}
			findings = append(findings, found...)
			counted, adopts, f := forFloor(o, s, label, declared, version)
			findings = append(findings, f...)
			if adopts {
				adopted[label] = true
			}
			if newer(counted, floor) {
				floor = counted
			}
			if version <= s.After || declared != "" {
				continue
			}
			found, err = checkFile(label, string(src))
			if err != nil {
				return nil, fmt.Errorf("%s: %w", label, err)
			}
			for i := range found {
				found[i].Upstream = s.Upstream
			}
			findings = append(findings, found...)
		}
	}
	stale := make([]string, 0, len(o.Adopted))
	for label := range o.Adopted {
		if !adopted[label] {
			stale = append(stale, label)
		}
	}
	sort.Strings(stale)
	for _, label := range stale {
		findings = append(findings, Finding{File: label, Reason: "is adopted, but no upstream set declares a contraction there past its history: remove it from Options.Adopted"})
	}
	if o.Floor != "" {
		f, err := checkFloor(o.Floor, floor)
		if err != nil {
			return nil, err
		}
		findings = append(findings, f...)
	}
	return findings, nil
}

// forFloor is the release a file's declaration counts for in the floor: its
// own for this repository's sets, the adopting product release for an
// upstream set's past its history, which therefore needs an adoption
// (adopts).
func forFloor(o Options, s Set, label, declared string, version int64) (counted string, adopts bool, findings []Finding) {
	switch {
	case s.Upstream == "":
		return declared, false, nil
	case declared == "" || version <= s.After:
		return "", false, nil
	}
	v, ok := o.Adopted[label]
	switch {
	case !ok:
		return "", true, []Finding{{File: label, Upstream: s.Upstream, Reason: fmt.Sprintf("contracts the schema in %s (its %s): adopt it in Options.Adopted under the first release of this product that ships it", s.Upstream, declared)}}
	case !release.MatchString(v):
		return "", true, []Finding{{File: label, Upstream: s.Upstream, Reason: fmt.Sprintf("is adopted under %q, which names no release (vX.Y.Z)", v)}}
	}
	return v, true, nil
}

// declaration is the release a file's contract comment names.
func declaration(label, src string) (string, []Finding) {
	m := contract.FindStringSubmatch(src)
	if m == nil {
		return "", nil
	}
	if !release.MatchString(m[1]) || strings.TrimSpace(m[2]) == "" {
		return "", []Finding{{File: label, Reason: "the contract comment must read -- contract: vX.Y.Z <why>"}}
	}
	return m[1], nil
}

func checkFloor(file, want string) ([]Finding, error) {
	b, err := os.ReadFile(file) //nolint:gosec // the test names its own repository's file
	if errors.Is(err, fs.ErrNotExist) {
		b, err = nil, nil
	}
	if err != nil {
		return nil, err
	}
	got := strings.TrimSpace(string(b))
	if got == want {
		return nil, nil
	}
	if want == "" {
		want = "nothing"
	}
	return []Finding{{File: file, Reason: fmt.Sprintf("the rollback floor is %q; the newest declaration or adoption makes it %s", got, want)}}, nil
}

// newer reports whether release a is above b; "" is below every release.
func newer(a, b string) bool {
	if a == "" {
		return false
	}
	if b == "" {
		return true
	}
	x, y := release.FindStringSubmatch(a), release.FindStringSubmatch(b)
	for i := 1; i <= 3; i++ {
		p, _ := strconv.Atoi(x[i])
		q, _ := strconv.Atoi(y[i])
		if p != q {
			return p > q
		}
	}
	return false
}
