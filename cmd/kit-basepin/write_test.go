package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func file(t *testing.T, name, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func read(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path) //nolint:gosec // the test's own file
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

const textRefOld = "registry.example/team/base:1.0@" + oldDigest

// A text file's pins are checked wherever a line names them: images of
// own and comments are skipped, one without a tag is listed unchecked.
func TestTextFilePinsAreChecked(t *testing.T) {
	reg := fakeRegistry(t, image{digest: newDigest, uploaded: now.Add(-10 * 24 * time.Hour)})
	makefile := file(t, "Makefile", "BASE = "+textRefOld+"\nimage: ${BASE_IMAGE:-"+textRefOld+"}\n# OLD = "+textRefOld+"\nOWN = ghcr.io/us/app:1@"+oldDigest+"\nRAW = registry.example/team/base@"+oldDigest+"\n")
	code, out := basepin(t, reg, "-own", "ghcr.io/us/", "-text", makefile)
	if code != 1 || strings.Count(out, "BEHIND") != 2 || !strings.Contains(out, "not checked") || strings.Contains(out, "ghcr.io/us") {
		t.Errorf("exit %d\n%s", code, out)
	}
}

// -write moves a pin past its wait in the line that names it and keeps one
// within its wait; a -fixes entry moves a pin within its wait at once.
func TestWriteMovesThePinsThatMustMove(t *testing.T) {
	recent := image{digest: newDigest, uploaded: now.Add(-2 * 24 * time.Hour)}
	for name, c := range map[string]struct {
		img   image
		fixes string
		moved bool
	}{
		"past its wait":      {image{digest: newDigest, uploaded: now.Add(-10 * 24 * time.Hour)}, "", true},
		"within its wait":    {recent, "", false},
		"a fix in the wait":  {recent, `[{"image":"registry.example/team/base","digest":"` + oldDigest + `","advisory":"CVE-2026-1","reason":"the tag's newest image drops it"}]`, true},
		"a fix nothing pins": {recent, `[{"image":"registry.example/other","digest":"` + oldDigest + `","advisory":"CVE-2026-1","reason":"x"}]`, false},
	} {
		script := file(t, "run.sh", "#!/bin/sh\ndocker run --rm "+textRefOld+" true\n")
		args := []string{"-write", "-text", script}
		if c.fixes != "" {
			args = append(args, "-fixes", file(t, "fixes.json", c.fixes))
		}
		code, out := basepin(t, fakeRegistry(t, c.img), args...)
		moved := strings.Contains(read(t, script), "@"+newDigest)
		if code != 0 || moved != c.moved || (moved && !strings.Contains(out, "moved to "+newDigest[:19])) {
			t.Errorf("%s: exit %d, moved %v\n%s", name, code, moved, out)
		}
	}
}

// -write drops the -advisories entries of the digests it moved and keeps
// the others as written.
func TestWriteDropsTheAdvisoriesOfMovedPins(t *testing.T) {
	reg := fakeRegistry(t, image{digest: newDigest, uploaded: now.Add(-2 * 24 * time.Hour)})
	dockerfile := file(t, "Dockerfile", "FROM "+textRefOld+"\n")
	advisories := file(t, "advisories.json", `[{"image":"registry.example/team/base","digest":"`+oldDigest+`","advisory":"DSA-1","reason":"r"}]`)
	code, out := basepin(t, reg, "-write", "-advisories", advisories, dockerfile)
	if code != 0 || !strings.Contains(read(t, dockerfile), newDigest) || strings.TrimSpace(read(t, advisories)) != "[]" {
		t.Errorf("exit %d\n%s\n%s", code, out, read(t, advisories))
	}
}
