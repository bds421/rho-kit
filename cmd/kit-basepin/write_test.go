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

// -write replaces a moved digest wherever the files read write it, as in
// a Makefile variable built from the tag, which no reader takes as a pin.
func TestWriteMovesTheDigestEverywhereItIsWritten(t *testing.T) {
	reg := fakeRegistry(t, image{digest: newDigest, uploaded: now.Add(-10 * 24 * time.Hour)})
	dockerfile := file(t, "Dockerfile", "FROM "+textRefOld+"\n")
	makefile := file(t, "Makefile", "REF = registry.example/team/base:$(V)@"+oldDigest+"\n")
	if code, out := basepin(t, reg, "-write", "-text", makefile, dockerfile); code != 0 || !strings.Contains(read(t, makefile), newDigest) || !strings.Contains(read(t, dockerfile), newDigest) {
		t.Errorf("exit %d\n%s", code, out)
	}
}

// -write leaves a pin whose new digest has no known age in place, lists it
// and exits 1, a build date alone included: a person looks.
func TestWriteLeavesADigestOfUnknownAge(t *testing.T) {
	for name, img := range map[string]image{
		"no push time":      {digest: newDigest, created: time.Unix(0, 0)},
		"a build date only": {digest: newDigest, created: now.Add(-9 * 24 * time.Hour)},
	} {
		script := file(t, "run.sh", "docker run "+textRefOld+"\n")
		code, out := basepin(t, fakeRegistry(t, img), "-write", "-text", script)
		if code != 1 || strings.Contains(read(t, script), newDigest) || !strings.Contains(out, "unknown age") {
			t.Errorf("%s: exit %d\n%s", name, code, out)
		}
	}
}

// A second -write finds every moved pin current and changes nothing.
func TestWriteTwiceChangesNothingTheSecondTime(t *testing.T) {
	reg := fakeRegistry(t, image{digest: newDigest, uploaded: now.Add(-10 * 24 * time.Hour)})
	script := file(t, "run.sh", "docker run "+textRefOld+"\n")
	if code, out := basepin(t, reg, "-write", "-text", script); code != 0 || !strings.Contains(out, "moved to") {
		t.Fatalf("first: exit %d\n%s", code, out)
	}
	after := read(t, script)
	if code, out := basepin(t, reg, "-write", "-text", script); code != 0 || strings.Contains(out, "moved to") || !strings.Contains(out, "current") || read(t, script) != after {
		t.Errorf("second: exit %d\n%s", code, out)
	}
}

// -verify runs for each move before anything is written: what it says is
// printed with the move, and a failure leaves every file as it was.
func TestWriteVerifiesEachNewDigestFirst(t *testing.T) {
	reg := fakeRegistry(t, image{digest: newDigest, uploaded: now.Add(-10 * 24 * time.Hour)})
	program := func(body string) string {
		path := file(t, "verify.sh", "#!/bin/sh\n"+body+"\n")
		if err := os.Chmod(path, 0o700); err != nil { //nolint:gosec // the test's own script
			t.Fatal(err)
		}
		return path
	}
	script := file(t, "run.sh", "docker run "+textRefOld+"\n")
	ok := program(`echo "publisher $1:$2 checked"`)
	if code, out := basepin(t, reg, "-write", "-verify", ok, "-text", script); code != 0 || !strings.Contains(out, "verified: publisher registry.example/team/base:1.0 checked") {
		t.Errorf("a passing check: exit %d\n%s", code, out)
	}
	script = file(t, "run.sh", "docker run "+textRefOld+"\n")
	if code, out := basepin(t, reg, "-write", "-verify", program(`echo "no signature" >&2; exit 1`), "-text", script); code != 2 || strings.Contains(read(t, script), newDigest) || !strings.Contains(out, "no signature") {
		t.Errorf("a failing check: exit %d\n%s", code, out)
	}
}
