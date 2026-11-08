package doctruth

import (
	"slices"
	"strings"
	"testing"
)

const hostsDoc = "# Install\n\n## Outbound hosts\n\n| `api.example-provider.io` | x |\n| `*.feeds.io` | y |\n\n## Later\n\n`late.io`\n"

func hostsRepository(t *testing.T, extra map[string]string) string {
	t.Helper()
	files := map[string]string{
		"go.mod":                    "module example.invalid/svc\n\ngo 1.26\n",
		"docs/INSTALL.md":           hostsDoc,
		"cmd/svc/main.go":           "package main\n\nimport _ \"example.invalid/svc/internal/attest\"\n\nfunc main() {}\n",
		"internal/attest/a.go":      "package attest\n\nconst A = \"https://api.example-provider.io/v1\"\nconst B = \"https://eu.feeds.io/x\"\nconst Link = \"https://docs.vendor.io/page\"\n",
		"internal/attest/a_test.go": "package attest\n\nconst T = \"https://only-in-tests.io\"\n",
		"internal/unused/u.go":      "package unused\n\nconst U = \"https://not-shipped.io\"\n",
		"internal/attest/r.go":      "package attest\n\nconst R = \"https://api.example.com https://x.test\"\n",
	}
	for k, v := range extra {
		files[k] = v
	}
	return repository(t, files)
}

func hostOptions(root string) HostOptions {
	return HostOptions{
		Root:      root,
		Declared:  []string{"api.example-provider.io", "*.feeds.io"},
		NotDialed: map[string]string{"docs.vendor.io": "a link"},
		Document:  "docs/INSTALL.md",
	}
}

func checkHosts(t *testing.T, o HostOptions) []string {
	t.Helper()
	f, err := CheckHosts(o)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestDeclaredAndListedHostsPass(t *testing.T) {
	if f := checkHosts(t, hostOptions(hostsRepository(t, nil))); len(f) > 0 {
		t.Fatal(f)
	}
}

func TestAShippedHostNotDeclaredFails(t *testing.T) {
	root := hostsRepository(t, map[string]string{"internal/attest/n.go": "package attest\n\nconst N = \"https://new.io/x\"\n"})
	f := checkHosts(t, hostOptions(root))
	if len(f) != 1 || !slices.ContainsFunc(f, func(s string) bool { return len(s) > 6 && s[:6] == "new.io" }) {
		t.Fatal(f)
	}
}

func TestAnUnnamedHostMustBeDeclared(t *testing.T) {
	o := hostOptions(hostsRepository(t, nil))
	o.Unnamed = map[string]string{"api.sdk-default.io": "the SDK's default"}
	f := checkHosts(t, o)
	want := []string{"api.sdk-default.io (the SDK's default) is neither declared nor named as not dialed"}
	if !slices.Equal(f, want) {
		t.Fatalf("got %q, want %q", f, want)
	}
}

func TestADeclaredHostOutsideTheSectionFails(t *testing.T) {
	o := hostOptions(hostsRepository(t, nil))
	o.Declared = append(o.Declared, "late.io")
	f := checkHosts(t, o)
	want := []string{`docs/INSTALL.md's "Outbound hosts" section does not list late.io`}
	if !slices.Equal(f, want) {
		t.Fatalf("got %q, want %q", f, want)
	}
}

func TestTheReleaseGOOSAndTagsChooseTheFiles(t *testing.T) {
	root := hostsRepository(t, map[string]string{
		"internal/attest/l_linux.go": "package attest\n\nconst L = \"https://linux-only.io\"\n",
		"internal/attest/w_webui.go": "//go:build webui\n\npackage attest\n\nconst W = \"https://tagged.io\"\n",
	})
	o := hostOptions(root)
	o.GOOS, o.Tags = "linux", []string{"webui"}
	f := checkHosts(t, o)
	if len(f) != 2 {
		t.Fatal(f)
	}
}

func TestAWorkspaceBesideTheModuleStillScansIt(t *testing.T) {
	root := hostsRepository(t, map[string]string{
		"go.work":              "go 1.26\n\nuse (\n\t.\n\t./other\n)\n",
		"other/go.mod":         "module example.invalid/other\n\ngo 1.26\n",
		"other/o.go":           "package other\n",
		"internal/attest/n.go": "package attest\n\nconst N = \"https://new.io/x\"\n",
	})
	f := checkHosts(t, hostOptions(root))
	if len(f) != 1 {
		t.Fatal(f)
	}
}

func TestACommentInAFencedBlockDoesNotEndTheSection(t *testing.T) {
	doc := "## Outbound hosts\n\n```sh\n# a comment\n```\n\n### Detail\n\n`a.io`\n\n## Next\n\n`b.io`\n"
	section := headedSection(doc, "Outbound hosts")
	if !strings.Contains(section, "`a.io`") || strings.Contains(section, "`b.io`") {
		t.Fatal(section)
	}
}

func TestAnEmbeddedFileIsScanned(t *testing.T) {
	root := hostsRepository(t, map[string]string{
		"internal/attest/e.go":          "package attest\n\nimport _ \"embed\"\n\n//go:embed defaults.json\nvar defaults string\n",
		"internal/attest/defaults.json": "{\"url\": \"https://embedded.io/x\"}\n",
	})
	f := checkHosts(t, hostOptions(root))
	if len(f) != 1 || !strings.HasPrefix(f[0], "embedded.io") {
		t.Fatal(f)
	}
}

func TestAFailingGoListSaysWhy(t *testing.T) {
	o := hostOptions(hostsRepository(t, nil))
	o.Packages = []string{"./nowhere/..."}
	o.Tags = []string{"bad tag"}
	if _, err := CheckHosts(o); err == nil || !strings.Contains(err.Error(), "go list") || strings.HasSuffix(err.Error(), "exit status 1") {
		t.Fatal(err)
	}
}

func TestAModuleNothingIsBuiltFromIsAnError(t *testing.T) {
	o := hostOptions(hostsRepository(t, nil))
	o.Modules = []string{"example.invalid/svc", "example.invalid/misspelt"}
	if _, err := CheckHosts(o); err == nil || !strings.Contains(err.Error(), "example.invalid/misspelt") {
		t.Fatal(err)
	}
}
