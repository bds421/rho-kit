package main

import (
	"os"
	"strings"
	"testing"
)

// A late finding in a pinned upstream image waits on upstream while the
// newest image of its tag still carries it: listed, recorded with that
// image and passed. Once the newest image drops it, it fails, as moving
// the pin fixes it; a finding without an upstream scan fails as before.
func TestALateFindingTheNewestImageStillCarriesWaitsOnUpstream(t *testing.T) {
	const pinned = "docker.io/example/db:2.0@sha256:1111111111111111111111111111111111111111111111111111111111111111"
	const newest = "sha256:2222222222222222222222222222222222222222222222222222222222222222"
	late := match("C1", "Critical", "2026-10-01", "")
	report := write(t, "db.json", `{"matches":[`+late+`],"source":{"type":"image","target":{"userInput":"registry:`+pinned+`"}}}`)
	scanOfTag := func(matches string) string {
		return write(t, "db-newest.json", `{"matches":[`+matches+`],"source":{"type":"image","target":{"userInput":"docker.io/example/db:2.0","manifestDigest":"`+newest+`"}}}`)
	}
	policy := write(t, "policy.json", proxyPolicy)
	baseline := write(t, "b.json", base(""))
	record := write(t, "record.json", "")

	code, out := gate(t, "-policy", policy, "-baseline", baseline, "-record", record, "-release", "v2",
		"-upstream", pinned+"="+scanOfTag(late), report)
	if code != 0 || !strings.Contains(out, "waits on upstream: docker.io/example/db:2.0@"+newest+" C1") {
		t.Errorf("still carried: exit %d\n%s", code, out)
	}
	if body, _ := os.ReadFile(record); !strings.Contains(string(body), `"upstream": "docker.io/example/db:2.0@`+newest+`"`) {
		t.Errorf("the record names no upstream image:\n%s", body)
	}

	if code, out := gate(t, "-policy", policy, "-baseline", baseline, "-upstream", pinned+"="+scanOfTag(""), report); code != 1 {
		t.Errorf("dropped by the newest image: exit %d\n%s", code, out)
	}
	if code, out := gate(t, "-policy", policy, "-baseline", baseline, report); code != 1 {
		t.Errorf("without an upstream scan: exit %d\n%s", code, out)
	}
	if code, _ := gate(t, "-upstream", "db.json", report); code != 2 {
		t.Errorf("an -upstream without its pinned image: exit %d", code)
	}
}

// An -upstream scan must be of the pinned repository by its tag: one by
// digest, of another tag or of another repository is refused, as every
// finding would wait.
func TestAnUpstreamScanOfAnythingButTheTagIsRefused(t *testing.T) {
	const pinned = "docker.io/example/db:2.0@sha256:1111111111111111111111111111111111111111111111111111111111111111"
	report := write(t, "db.json", `{"matches":[],"source":{"type":"image","target":{"userInput":"registry:`+pinned+`"}}}`)
	for name, input := range map[string]string{
		"by digest":          pinned,
		"another tag":        "docker.io/example/db:2.1",
		"another repository": "docker.io/example/other:2.0",
		"no input":           "",
	} {
		scan := write(t, "db-newest.json", `{"matches":[],"source":{"type":"image","target":{"userInput":"`+input+`"}}}`)
		if code, out := gate(t, "-upstream", pinned+"="+scan, report); code != 2 || !strings.Contains(out, "not of the pinned image's tag") {
			t.Errorf("%s: exit %d\n%s", name, code, out)
		}
	}
	scan := write(t, "db-newest.json", `{"matches":[],"source":{"type":"image","target":{"userInput":"example/db:2.0"}}}`)
	if code, out := gate(t, "-upstream", pinned+"="+scan, report); code != 0 {
		t.Errorf("Docker Hub's short name: exit %d\n%s", code, out)
	}
}
