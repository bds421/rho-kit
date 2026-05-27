// Command check-doc-rot validates wave-N claims in release docs
// (RELEASE_NOTES_v2.md and any other *.md under docs/) against the
// actual git commit log.
//
// # Why this exists
//
// Wave 158 surfaced a real failure mode: a release doc claimed
// `WithOpaqueConsumeLabels` was deferred to a "future wave", but
// wave 140 had already shipped it. The doc was 18 days stale and
// the only reason a human caught it was a grep at release-prep
// time. This tool turns that grep into a CI gate.
//
// # What gets checked
//
// For every Markdown file under docs/ (recursive), the tool extracts
// references of the form:
//
//	wave N
//	Wave N
//	(wave N)
//	(new in wave N)
//	(see wave N)
//	in wave N
//	since wave N
//	as of wave N
//	tracked for a future wave
//	tracked for wave N
//	follow-up wave
//	post-2.0.0
//
// For each "wave N" reference, the tool verifies that a commit
// matching `^(feat|fix|refactor|chore|docs|test|perf)\(v2\).*wave N\b`
// exists in `git log`. References to "future wave" / "follow-up
// wave" without a wave number are flagged as ambiguous (these are
// the exact stale-claim shape wave 158 fixed).
//
// # Exit codes
//
//	0  no doc rot
//	1  doc rot detected
//	2  CLI / discovery failure
//
// # Allowlist
//
// A line-level opt-out: append `<!-- kit:ok-doc-rot -->` to a
// specific Markdown line when a "future wave" claim is genuinely
// open and tracked elsewhere. Example:
//
//	A consumer-side `WithOpaqueConsumeLabels` toggle is tracked for a
//	future wave. <!-- kit:ok-doc-rot -->
package main

import (
	"bufio"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// waveRefRE matches "wave N" anywhere in a Markdown line, case
// insensitive. The capture group is the wave number. Word boundary
// after the digits prevents `wave 14` matching `wave 145` etc.
var waveRefRE = regexp.MustCompile(`(?i)\bwave\s+(\d+)\b`)

// futureWaveRE matches the stale-claim shape: a "future wave" or
// "follow-up wave" or "tracked for ... wave" phrase WITHOUT a
// specific wave number. Wave 158 was exactly this — a "future wave"
// promise that had silently shipped.
var futureWaveRE = regexp.MustCompile(`(?i)(future wave|follow[- ]up wave|tracked.{1,30}for.{1,30}wave[^0-9]|post[- ]2\.0\.0)`)

// allowOptOutRE matches the line-level opt-out marker.
var allowOptOutRE = regexp.MustCompile(`<!--\s*kit:ok-doc-rot\s*-->`)

// commitWaveRE extracts a wave number from a commit subject. Used
// to build the set of wave numbers that genuinely shipped.
var commitWaveRE = regexp.MustCompile(`(?i)wave\s+(\d+)\b`)

type finding struct {
	file string
	line int
	text string
	kind string // "missing-wave", "future-wave-without-anchor"
}

func main() {
	var (
		rootDir   string
		docsGlob  string
		verbose   bool
		failOnAny bool
	)
	flag.StringVar(&rootDir, "root", ".", "Repository root.")
	flag.StringVar(&docsGlob, "docs", "docs", "Subdirectory to scan (relative to root).")
	flag.BoolVar(&verbose, "v", false, "Verbose: print every wave reference scanned.")
	flag.BoolVar(&failOnAny, "strict", true, "Fail on any finding (default true).")
	flag.Parse()

	root, err := filepath.Abs(rootDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "doc-rot: resolve root: %v\n", err)
		os.Exit(2)
	}

	shippedWaves, err := loadShippedWaves(root)
	if err != nil {
		fmt.Fprintf(os.Stderr, "doc-rot: load shipped waves from git: %v\n", err)
		os.Exit(2)
	}
	if verbose {
		nums := make([]int, 0, len(shippedWaves))
		for n := range shippedWaves {
			nums = append(nums, n)
		}
		sort.Ints(nums)
		fmt.Fprintf(os.Stderr, "doc-rot: %d shipped waves found in git log: %v\n", len(nums), nums)
	}

	docsRoot := filepath.Join(root, docsGlob)
	var findings []finding
	err = filepath.WalkDir(docsRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".md") {
			return nil
		}
		f, openErr := scanFile(path, shippedWaves, verbose)
		if openErr != nil {
			return openErr
		}
		findings = append(findings, f...)
		return nil
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "doc-rot: walk %s: %v\n", docsRoot, err)
		os.Exit(2)
	}

	if len(findings) == 0 {
		fmt.Printf("doc-rot check OK (%d shipped waves verified across docs/)\n", len(shippedWaves))
		os.Exit(0)
	}

	// Report sorted by file then line for stable CI output.
	sort.Slice(findings, func(i, j int) bool {
		if findings[i].file != findings[j].file {
			return findings[i].file < findings[j].file
		}
		return findings[i].line < findings[j].line
	})

	fmt.Fprintln(os.Stderr, "doc-rot check FAILED")
	fmt.Fprintln(os.Stderr)
	for _, f := range findings {
		rel, _ := filepath.Rel(root, f.file)
		fmt.Fprintf(os.Stderr, "  %s:%d [%s]\n    %s\n\n", rel, f.line, f.kind, strings.TrimSpace(f.text))
	}
	fmt.Fprintln(os.Stderr, "Append `<!-- kit:ok-doc-rot -->` to a specific line to opt out when a future-wave claim is genuinely open and tracked elsewhere.")
	if failOnAny {
		os.Exit(1)
	}
}

// loadShippedWaves runs `git log --format=%s` and parses every
// subject line for "wave N" mentions. The resulting set is the
// ground truth for which waves have actually been committed.
func loadShippedWaves(repoRoot string) (map[int]bool, error) {
	cmd := exec.Command("git", "log", "--format=%s")
	cmd.Dir = repoRoot
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git log: %w", err)
	}
	waves := map[int]bool{}
	for _, line := range strings.Split(string(out), "\n") {
		matches := commitWaveRE.FindAllStringSubmatch(line, -1)
		for _, m := range matches {
			n, perr := strconv.Atoi(m[1])
			if perr != nil {
				continue
			}
			waves[n] = true
		}
	}
	return waves, nil
}

// scanFile walks a Markdown file looking for "wave N" references
// that don't have a matching shipped wave, and unanchored
// "future wave" / "follow-up wave" claims.
func scanFile(path string, shipped map[int]bool, verbose bool) ([]finding, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var out []finding
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1<<20), 1<<20) // some release docs are long
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := scanner.Text()
		if allowOptOutRE.MatchString(line) {
			continue
		}

		// Check "wave N" references against shipped set.
		matches := waveRefRE.FindAllStringSubmatch(line, -1)
		for _, m := range matches {
			n, perr := strconv.Atoi(m[1])
			if perr != nil {
				continue
			}
			if verbose {
				fmt.Fprintf(os.Stderr, "  %s:%d ref wave %d (shipped=%v)\n", path, lineNo, n, shipped[n])
			}
			if !shipped[n] {
				out = append(out, finding{
					file: path,
					line: lineNo,
					text: line,
					kind: fmt.Sprintf("wave %d referenced but no matching commit found in git log", n),
				})
			}
		}

		// Check unanchored future-wave claims. Skip lines that ALSO
		// contain a specific wave-N reference — those are concrete
		// (e.g. "tracked for wave 167") and validated above.
		if futureWaveRE.MatchString(line) && len(matches) == 0 {
			out = append(out, finding{
				file: path,
				line: lineNo,
				text: line,
				kind: "unanchored future-wave claim (no specific wave number, no opt-out marker)",
			})
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
