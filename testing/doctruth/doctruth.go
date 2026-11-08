// Package doctruth keeps a repository's documentation true to its tree, as
// a test: every folder the architecture document's package section names
// exists, every package is named there, and every repository path the
// documents cite resolves. Run it from one test in the repository:
//
//	func TestTheDocsAreTrue(t *testing.T) {
//		doctruth.Run(t, doctruth.Options{
//			Root:              "../..",
//			OtherRepositories: regexp.MustCompile(`(?i)other-service|shared toolkit`),
//		})
//	}
package doctruth

import (
	"regexp"
	"testing"
)

// Options says where the documents are and what they may cite elsewhere.
type Options struct {
	// Root is the repository root. It must be a git working tree: the
	// documents are the tracked ones, and a cited path git ignores (a build
	// output) counts as resolved.
	Root string
	// Architecture is the document with the package section, relative to
	// Root. Default docs/ARCHITECTURE.md.
	Architecture string
	// Sections are the headings (level 2) of the package section, the first
	// present one counts. It is a tree in code blocks or a table whose first
	// cell names a package. Default "Folder structure", "Packages".
	Sections []string
	// PackageRoots are the folders whose Go packages must be named.
	// Default cmd, internal.
	PackageRoots []string
	// Unnamed matches the name of a package that needs no line of its own:
	// test fixtures and generated code. Default `^gen$|test$`.
	Unnamed *regexp.Regexp
	// Documents chooses the tracked Markdown files whose cited paths must
	// resolve. Default: the architecture and security documents in docs/,
	// the ADRs in docs/adr/, every README.md and the top-level deploy/*.md.
	Documents func(path string) bool
	// OtherRepositories matches the names of other repositories. A line
	// that names one, the line after it, and a section whose heading or
	// bold lead names one cite that repository's files and are skipped.
	OtherRepositories *regexp.Regexp
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

// Check returns what in the documentation is not true to the tree.
func Check(o Options) ([]string, error) {
	o = o.withDefaults()
	section, err := packageSection(o)
	if err != nil {
		return nil, err
	}
	findings := missingFolders(o.Root, section)
	unnamed, err := unnamedPackages(o, section)
	if err != nil {
		return nil, err
	}
	findings = append(findings, unnamed...)
	unresolved, err := unresolvedCitations(o)
	if err != nil {
		return nil, err
	}
	return append(findings, unresolved...), nil
}

func (o Options) withDefaults() Options {
	if o.Architecture == "" {
		o.Architecture = "docs/ARCHITECTURE.md"
	}
	if len(o.Sections) == 0 {
		o.Sections = []string{"Folder structure", "Packages"}
	}
	if len(o.PackageRoots) == 0 {
		o.PackageRoots = []string{"cmd", "internal"}
	}
	if o.Unnamed == nil {
		o.Unnamed = regexp.MustCompile(`^gen$|test$`)
	}
	if o.Documents == nil {
		o.Documents = defaultDocuments
	}
	return o
}
