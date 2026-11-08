package doctruth

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"testing"
)

const treeArchitecture = "# Architecture\n\n## Folder structure\n\n```\n" +
	"cmd/tool/               the command\n" +
	"internal/\n" +
	"  store/                the store\n" +
	"    wire/entity/ report/   shapes\n" +
	"  <module>/postgres/    each module's store\n" +
	"```\n\n## Next\n\nPast the section.\n"

const tableArchitecture = "# Architecture\n\n## Packages\n\n| Package | Owns |\n|---|---|\n" +
	"| `cmd/tool` | the command |\n" +
	"| `internal/store`, `billing` | the store; billing |\n" +
	"| `internal/store/wire/{entity,report}` | shapes |\n"

func repository(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, content := range files {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{{"init", "-q"}, {"add", "-A"}} {
		if out, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	return root
}

func goPackages(files map[string]string, pkgs ...string) map[string]string {
	for _, p := range pkgs {
		files[p+"/x.go"] = "package " + filepath.Base(p) + "\n"
	}
	return files
}

func check(t *testing.T, o Options) []string {
	t.Helper()
	findings, err := Check(o)
	if err != nil {
		t.Fatal(err)
	}
	return findings
}

func TestATrueTreePasses(t *testing.T) {
	root := repository(t, goPackages(map[string]string{"docs/ARCHITECTURE.md": treeArchitecture},
		"cmd/tool", "internal/store", "internal/store/wire/entity", "internal/store/wire/report",
		"internal/billing/postgres", "internal/store/storetest", "internal/store/gen"))
	if f := check(t, Options{Root: root}); len(f) > 0 {
		t.Fatal(f)
	}
}

func TestATrueTablePasses(t *testing.T) {
	root := repository(t, goPackages(map[string]string{"docs/ARCHITECTURE.md": tableArchitecture},
		"cmd/tool", "internal/store", "internal/billing", "internal/store/wire", "internal/store/wire/entity", "internal/store/wire/report"))
	if f := check(t, Options{Root: root}); len(f) > 0 {
		t.Fatal(f)
	}
}

func TestAFolderThatIsGoneFails(t *testing.T) {
	root := repository(t, goPackages(map[string]string{"docs/ARCHITECTURE.md": treeArchitecture},
		"cmd/tool", "internal/store", "internal/store/wire/entity"))
	f := check(t, Options{Root: root})
	if !slices.Contains(f, "the package section names internal/store/report, which does not exist") {
		t.Fatal(f)
	}
}

func TestAPackageTheSectionDoesNotNameFails(t *testing.T) {
	root := repository(t, goPackages(map[string]string{"docs/ARCHITECTURE.md": tableArchitecture},
		"cmd/tool", "internal/store", "internal/billing", "internal/store/wire", "internal/store/wire/entity", "internal/store/wire/report",
		"internal/next"))
	f := check(t, Options{Root: root})
	if !slices.Equal(f, []string{"the package section does not name the package internal/next"}) {
		t.Fatal(f)
	}
}

func TestACitedPathThatIsGoneFails(t *testing.T) {
	root := repository(t, goPackages(map[string]string{
		"docs/ARCHITECTURE.md": tableArchitecture,
		".gitignore":           "dist/\n",
		"docs/adr/0001-x.md": "# 1. X\n\n`internal/store` exists, `internal/gone.go` not.\n" +
			"A build output `dist/stats.html`, a tag `cmd/tool/vX.Y.Z`.\n" +
			"The other\nservice's `internal/theirs.go`.\n\n" +
			"**In the other service**\n\n- `internal/theirs/a.go`\n\n## Back here\n\n`cmd/missing`\n",
		"deploy/sub/NOTES.md": "`internal/not-a-document`\n",
	}, "cmd/tool", "internal/store", "internal/billing", "internal/store/wire", "internal/store/wire/entity", "internal/store/wire/report"))
	f := check(t, Options{Root: root, OtherRepositories: regexp.MustCompile(`(?i)other service`)})
	want := []string{
		"docs/adr/0001-x.md:3 cites internal/gone.go, which does not exist",
		"docs/adr/0001-x.md:14 cites cmd/missing, which does not exist",
	}
	if !slices.Equal(f, want) {
		t.Fatalf("got %q, want %q", f, want)
	}
}

func TestAMissingSectionIsAnError(t *testing.T) {
	root := repository(t, map[string]string{"docs/ARCHITECTURE.md": "# Architecture\n"})
	if _, err := Check(Options{Root: root}); err == nil {
		t.Fatal("no error")
	}
}
