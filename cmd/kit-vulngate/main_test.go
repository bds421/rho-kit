package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var now = time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)

func match(id, severity, fixedOn string, extra string) string {
	fix := `{"state":"not-fixed"}`
	if fixedOn != "" {
		fix = `{"state":"fixed","versions":["9.9.9"],"available":[{"version":"9.9.9","date":"` + fixedOn + `"}]}`
	}
	return `{"vulnerability":{"id":"` + id + `","severity":"` + severity + `","fix":` + fix + extra + `},"artifact":{"name":"pkg-` + id + `","version":"1.0.0"}}`
}

func write(t *testing.T, name, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func gate(t *testing.T, args ...string) (int, string) {
	t.Helper()
	var out, errs bytes.Buffer
	code := run(args, &out, &errs, now)
	return code, out.String() + errs.String()
}

// Each severity has its deadline from the day its fix was published; one
// actively exploited has 72 hours; a low one ships with the next release.
func TestFixedVulnerabilitiesAreHeldToTheirDeadlines(t *testing.T) {
	for name, c := range map[string]struct {
		match string
		late  bool
	}{
		"critical after 8 days":   {match("C1", "Critical", "2026-10-28", ""), true},
		"critical after 3 days":   {match("C2", "Critical", "2026-11-02", ""), false},
		"high after 10 days":      {match("H1", "High", "2026-10-26", ""), false},
		"high after 31 days":      {match("H2", "High", "2026-10-05", ""), true},
		"medium after 60 days":    {match("M1", "Medium", "2026-09-06", ""), false},
		"low with a fix":          {match("L1", "Low", "2026-11-04", ""), true},
		"exploited after 4 days":  {match("K1", "Medium", "2026-11-01", `,"knownExploited":[{"cve":"CVE-2026-1"}]`), true},
		"no fix":                  {match("N1", "Critical", "", ""), false},
		"a fix without its date":  {`{"vulnerability":{"id":"D1","severity":"High","fix":{"state":"fixed","versions":["2"]}},"artifact":{"name":"d","version":"1"}}`, true},
		"no severity, 100 days":   {match("U1", "Unknown", "2026-07-28", ""), true},
		"related severity counts": {`{"vulnerability":{"id":"G1","severity":"Unknown","fix":{"state":"fixed","versions":["2"],"available":[{"version":"2","date":"2026-10-20"}]}},"relatedVulnerabilities":[{"id":"CVE-2026-2","severity":"Critical"}],"artifact":{"name":"g","version":"1"}}`, true},
	} {
		code, out := gate(t, write(t, "image.json", `{"matches":[`+c.match+`]}`))
		if (code == 1) != c.late || code == 2 {
			t.Errorf("%s: exit %d\n%s", name, code, out)
		}
	}
}

// An accepted finding passes until its date; then it counts again.
func TestAnAcceptedFindingPassesUntilItsDate(t *testing.T) {
	report := write(t, "image.json", `{"matches":[`+match("C1", "Critical", "2026-10-01", "")+`]}`)
	accept := func(until string) string {
		return write(t, "accepted.json", `[{"id":"C1","package":"pkg-C1","reason":"not reached (govulncheck)","until":"`+until+`"}]`)
	}
	if code, out := gate(t, "-accepted", accept("2026-11-05"), report); code != 0 || !strings.Contains(out, "accepted") {
		t.Errorf("accepted through today: exit %d\n%s", code, out)
	}
	if code, _ := gate(t, "-accepted", accept("2026-11-04"), report); code != 1 {
		t.Errorf("an expired acceptance: exit %d", code)
	}
	if code, _ := gate(t, "-accepted", write(t, "bad.json", `[{"id":"C1"}]`), report); code != 2 {
		t.Errorf("an acceptance without reason and date: exit %d", code)
	}
}

func TestBadInputIsNoPass(t *testing.T) {
	if code, _ := gate(t); code != 2 {
		t.Errorf("no report: exit %d", code)
	}
	if code, _ := gate(t, write(t, "x.json", "not json")); code != 2 {
		t.Errorf("not a report: exit %d", code)
	}
	if code, _ := gate(t, filepath.Join(t.TempDir(), "missing.json")); code != 2 {
		t.Errorf("a missing report: exit %d", code)
	}
}

// A named acceptance file that is missing is bad input, not an empty list.
func TestAMissingAcceptanceFileIsNoPass(t *testing.T) {
	report := write(t, "image.json", `{"matches":[`+match("C1", "Critical", "2026-11-04", "")+`]}`)
	if code, out := gate(t, "-accepted", filepath.Join(t.TempDir(), "missing.json"), report); code != 2 {
		t.Errorf("a missing acceptance file: exit %d\n%s", code, out)
	}
}

// Each deadline can be set; a negative one is refused.
func TestDeadlinesCanBeSet(t *testing.T) {
	report := write(t, "image.json", `{"matches":[`+match("H1", "High", "2026-10-26", "")+`,`+match("L1", "Low", "2026-11-04", "")+`]}`)
	if code, out := gate(t, report); code != 1 {
		t.Fatalf("defaults: exit %d\n%s", code, out)
	}
	if code, out := gate(t, "-low", "720h", report); code != 0 {
		t.Errorf("-low 720h: exit %d\n%s", code, out)
	}
	if code, out := gate(t, "-low", "720h", "-high", "168h", report); code != 1 || !strings.Contains(out, "LATE") {
		t.Errorf("-high 168h: exit %d\n%s", code, out)
	}
	if code, _ := gate(t, "-kev", "-1h", report); code != 2 {
		t.Errorf("a negative deadline: exit %d", code)
	}
}

// An acceptance with an image passes the finding only in that image: by
// the digest a registry scan names, or the one given for an archive's
// report; another digest, an unnamed image or an SBOM counts it again.
func TestAnAcceptanceWithAnImageAppliesToThatImageOnly(t *testing.T) {
	const digest = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
	const other = "sha256:2222222222222222222222222222222222222222222222222222222222222222"
	scan := func(name, userInput string) string {
		return write(t, name, `{"matches":[`+match("C1", "Critical", "2026-10-01", "")+`],"source":{"type":"image","target":{"userInput":"`+userInput+`"}}}`)
	}
	accepted := write(t, "accepted.json", `[{"id":"C1","package":"pkg-C1","image":"example/db:1.2@`+digest+`","reason":"not reached","until":"2026-12-01"}]`)
	for name, c := range map[string]struct {
		args []string
		pass bool
	}{
		"that image":                     {[]string{scan("a.json", "example/db:1.2@"+digest)}, true},
		"that image, Docker Hub spelled": {[]string{scan("b.json", "registry:docker.io/example/db@"+digest)}, true},
		"another digest":                 {[]string{scan("c.json", "example/db:1.2@"+other)}, false},
		"another image":                  {[]string{scan("d.json", "example/cache:1.2@"+digest)}, false},
		"an archive":                     {[]string{scan("e.json", "/build/db.oci.tar")}, false},
		"an archive built from it":       {[]string{"example/db:1.2@" + digest + "=" + scan("f.json", "/build/db.oci.tar")}, true},
	} {
		code, out := gate(t, append([]string{"-accepted", accepted}, c.args...)...)
		if (code == 0) != c.pass || code == 2 {
			t.Errorf("%s: exit %d\n%s", name, code, out)
		}
	}
	if code, _ := gate(t, "-accepted", write(t, "bad.json", `[{"id":"C1","package":"p","image":"example/db:1.2","reason":"r","until":"2026-12-01"}]`), scan("g.json", "x")); code != 2 {
		t.Errorf("an image without digest: exit %d", code)
	}
	if code, _ := gate(t, "example/db:1.2="+scan("h.json", "x")); code != 2 {
		t.Errorf("an archive's image without digest: exit %d", code)
	}
}

// Two reports of one name would be listed as one target, so a clean one
// could stand for a dirty one: refused.
func TestTwoReportsOfOneNameAreRefused(t *testing.T) {
	clean := write(t, "db.json", `{"matches":[]}`)
	dirty := write(t, "db.json", `{"matches":[`+match("C1", "Critical", "2026-10-01", "")+`]}`)
	if code, out := gate(t, clean, dirty); code != 2 || !strings.Contains(out, "two reports are named db") {
		t.Errorf("exit %d\n%s", code, out)
	}
}
