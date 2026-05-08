package asvs

import (
	"bufio"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Annotation pairs an ASVS [ID] with the source file + line where the
// kit's `// asvs: ...` comment appeared. kit-doctor uses these to
// produce a per-package matrix of which controls a service claims to
// satisfy.
type Annotation struct {
	ID   ID
	File string
	Line int
}

// ScanReport is the output of [ScanDir]: every annotation found, the
// distinct set of IDs claimed (deduplicated across files), the
// catalog IDs that no annotation referenced, and any annotation IDs
// that don't resolve to a [Catalog] entry (likely typos).
type ScanReport struct {
	Annotations []Annotation
	Claimed     []ID
	Missing     []ID
	Unknown     []ID
}

// ScanDir walks root looking for "// asvs: ..." annotations in .go
// files. Vendor directories and _test.go files are skipped — the
// kit's claim is "production code on the request path satisfies
// this control"; tests are documentation, not enforcement.
//
// Returns a [ScanReport] suitable for kit-doctor rendering. An error
// is returned only on filesystem failure; an annotation pointing at
// an unknown ID is reported via ScanReport.Unknown, not an error,
// because the caller may want to surface it differently.
func ScanDir(root string) (ScanReport, error) {
	var anns []Annotation
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			name := d.Name()
			if name == "vendor" || strings.HasPrefix(name, ".") || name == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		fileAnns, err := scanFile(path)
		if err != nil {
			return err
		}
		anns = append(anns, fileAnns...)
		return nil
	})
	if err != nil {
		return ScanReport{}, fmt.Errorf("asvs: walk %q: %w", root, err)
	}
	return buildReport(anns), nil
}

func scanFile(path string) ([]Annotation, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("asvs: open %q: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	var out []Annotation
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		ids := ParseAnnotation(scanner.Text())
		for _, id := range ids {
			out = append(out, Annotation{ID: id, File: path, Line: lineNo})
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("asvs: scan %q: %w", path, err)
	}
	return out, nil
}

func buildReport(anns []Annotation) ScanReport {
	claimedSet := map[ID]struct{}{}
	unknownSet := map[ID]struct{}{}
	for _, a := range anns {
		claimedSet[a.ID] = struct{}{}
		if _, err := Lookup(a.ID); err != nil {
			unknownSet[a.ID] = struct{}{}
		}
	}

	missingSet := map[ID]struct{}{}
	for _, c := range Catalog {
		if _, ok := claimedSet[c.ID]; !ok {
			missingSet[c.ID] = struct{}{}
		}
	}

	return ScanReport{
		Annotations: anns,
		Claimed:     sortedIDs(claimedSet),
		Missing:     sortedIDs(missingSet),
		Unknown:     sortedIDs(unknownSet),
	}
}

func sortedIDs(set map[ID]struct{}) []ID {
	out := make([]ID, 0, len(set))
	for id := range set {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
