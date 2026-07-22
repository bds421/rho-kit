package main

import (
	"bufio"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var suppressionFieldRE = regexp.MustCompile(`([a-z_]+)="([^"]*)"`)

// suppression is the attributable inventory record for an inline rule
// exception. Legacy suppressions are retained but marked incomplete so CI can
// progressively govern them without silently changing runtime policy.
type suppression struct {
	Rule            string `json:"rule"`
	File            string `json:"file"`
	Line            int    `json:"line"`
	Owner           string `json:"owner,omitempty"`
	Reason          string `json:"reason,omitempty"`
	ReviewDate      string `json:"review_date,omitempty"`
	SecurityPosture string `json:"security_posture,omitempty"`
	Complete        bool   `json:"complete"`
}

// suppressionInventory finds every inline kit-doctor allowance. The supported
// metadata shape is:
//
//	// kit-doctor:allow rule owner="team" reason="why" review="YYYY-MM-DD" posture="security|unchanged"
func suppressionInventory(root string) ([]suppression, error) {
	var out []suppression
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "vendor" || d.Name() == "node_modules" || (strings.HasPrefix(d.Name(), ".") && path != root) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		scanner := bufio.NewScanner(f)
		for line := 1; scanner.Scan(); line++ {
			entry, ok := parseSuppression(path, line, scanner.Text())
			if ok {
				out = append(out, entry)
			}
		}
		scanErr := scanner.Err()
		closeErr := f.Close()
		if scanErr != nil {
			return scanErr
		}
		return closeErr
	})
	return out, err
}

func parseSuppression(path string, line int, text string) (suppression, bool) {
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, "// kit-doctor:allow ") {
		return suppression{}, false
	}
	rest := strings.TrimSpace(strings.TrimPrefix(text, "// kit-doctor:allow "))
	fields := strings.Fields(rest)
	if len(fields) == 0 {
		return suppression{}, false
	}
	s := suppression{Rule: fields[0], File: path, Line: line}
	for _, match := range suppressionFieldRE.FindAllStringSubmatch(rest, -1) {
		switch match[1] {
		case "owner":
			s.Owner = match[2]
		case "reason":
			s.Reason = match[2]
		case "review":
			s.ReviewDate = match[2]
		case "posture":
			s.SecurityPosture = match[2]
		}
	}
	s.Complete = s.Owner != "" && s.Reason != "" && s.ReviewDate != "" && (s.SecurityPosture == "security" || s.SecurityPosture == "unchanged")
	return s, true
}
