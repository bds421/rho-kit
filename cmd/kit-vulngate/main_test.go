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
// actively exploited has 72 hours; low, medium and none 90 days.
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
		"low after 30 days":       {match("L1", "Low", "2026-10-06", ""), false},
		"low after 91 days":       {match("L2", "Low", "2026-08-06", ""), true},
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

// The policy file sets each deadline, in days or as a duration; an
// unknown key or a negative deadline is refused.
func TestDeadlinesAreSetByThePolicyFile(t *testing.T) {
	report := write(t, "image.json", `{"matches":[`+match("H1", "High", "2026-10-26", "")+`,`+match("L1", "Low", "2026-10-06", "")+`]}`)
	policy := func(deadlines string) string {
		return write(t, "policy.json", `{"deadlines":{`+deadlines+`}}`)
	}
	if code, out := gate(t, report); code != 0 {
		t.Fatalf("defaults: exit %d\n%s", code, out)
	}
	if code, out := gate(t, "-policy", policy(`"low":"14d"`), report); code != 1 || !strings.Contains(out, "LATE: due 2026-10-20") {
		t.Errorf("low 14d: exit %d\n%s", code, out)
	}
	if code, out := gate(t, "-policy", policy(`"high":"168h"`), report); code != 1 || !strings.Contains(out, "LATE: due 2026-11-02") {
		t.Errorf("high 168h: exit %d\n%s", code, out)
	}
	if code, out := gate(t, "-policy", policy(`"kev":"-1h"`), report); code != 2 {
		t.Errorf("a negative deadline: exit %d\n%s", code, out)
	}
	if code, out := gate(t, "-policy", policy(`"severe":"7d"`), report); code != 2 {
		t.Errorf("an unknown deadline: exit %d\n%s", code, out)
	}
	if code, out := gate(t, "-policy", write(t, "policy.json", `{"deadline":{}}`), report); code != 2 {
		t.Errorf("an unknown key: exit %d\n%s", code, out)
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

// proxy is an image of ours with a third-party binary in it: the binary's
// Go modules are the component's, the image's apk packages ours.
const proxyPolicy = `{"thirdParty":[{"component":"server","target":"proxy-*","path":"/usr/bin/server"},{"component":"db","image":"example/db"}]}`

func inBinary(id, fixedOn, path string) string {
	m := match(id, "High", fixedOn, "")
	return strings.Replace(m, `"version":"1.0.0"}`, `"version":"1.0.0","locations":[{"path":"`+path+`"}]}`, 1)
}

// A third-party finding the previous release carried is tracked, not
// failed, under its ID or an alias; one this release adds is held to its
// deadline from the package's fix; one in what we own is held to it
// whether the previous release had it or not.
func TestThirdPartyFindingsFailOnlyWhenNew(t *testing.T) {
	policy := write(t, "policy.json", proxyPolicy)
	old := "2026-09-01"
	baseline := write(t, "baseline.json", `[{"component":"server","package":"pkg-H1","id":"H1"},{"component":"server","package":"pkg-H2","id":"CVE-2026-9"},{"component":"server","package":"pkg-O1","id":"O1"}]`)
	for name, c := range map[string]struct {
		match string
		pass  bool
		state string
	}{
		"inherited, unchanged":     {inBinary("H1", old, "/usr/bin/server"), true, "tracked: due once server ships the fix (package fixed 2026-09-01, 2026-10-01 counted from it)"},
		"inherited under an alias": {strings.Replace(inBinary("H2", old, "/usr/bin/server"), `"artifact"`, `"relatedVulnerabilities":[{"id":"CVE-2026-9","severity":"High"}],"artifact"`, 1), true, "tracked"},
		"new in the component":     {inBinary("H3", old, "/usr/bin/server"), false, "LATE: new, due 2026-10-01"},
		"new, within its deadline": {inBinary("H4", "2026-11-01", "/usr/bin/server"), true, "new, due 2026-12-01"},
		"new, without a fix":       {inBinary("H5", "", "/usr/bin/server"), true, "no fix yet"},
		"ours, in the same image":  {inBinary("O1", old, "/lib/apk/db/installed"), false, "own  LATE: due 2026-10-01"},
	} {
		report := write(t, "proxy-1.2.json", `{"matches":[`+c.match+`]}`)
		code, out := gate(t, "-policy", policy, "-baseline", baseline, report)
		if (code == 0) != c.pass || code == 2 || !strings.Contains(out, c.state) {
			t.Errorf("%s: exit %d\n%s", name, code, out)
		}
	}
}

// An upstream image is a component by its repository, whichever digest.
func TestAnUpstreamImageIsAComponent(t *testing.T) {
	const digest = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
	report := write(t, "db.json", `{"matches":[`+match("C1", "Critical", "2026-10-01", "")+`],"source":{"type":"image","target":{"userInput":"registry:docker.io/example/db:2.0@`+digest+`"}}}`)
	policy := write(t, "policy.json", proxyPolicy)
	if code, out := gate(t, "-policy", policy, "-baseline", write(t, "b.json", `[{"component":"db","package":"pkg-C1","id":"C1"}]`), report); code != 0 || !strings.Contains(out, "db  tracked") {
		t.Errorf("inherited: exit %d\n%s", code, out)
	}
	if code, out := gate(t, "-policy", policy, "-baseline", write(t, "b.json", `[]`), report); code != 1 {
		t.Errorf("new: exit %d\n%s", code, out)
	}
}

// -record writes the third-party findings once each, sorted, when the
// gate passes, and nothing when it fails; the record is the next run's
// baseline.
func TestARecordIsTheNextBaseline(t *testing.T) {
	policy := write(t, "policy.json", proxyPolicy)
	recorded := filepath.Join(t.TempDir(), "baseline.json")
	fresh := write(t, "proxy-1.amd64.json", `{"matches":[`+inBinary("H4", "2026-11-01", "/usr/bin/server")+`,`+inBinary("H3", "2026-11-01", "/usr/bin/server")+`,`+inBinary("O1", "2026-11-01", "/lib/apk/db/installed")+`]}`)
	twin := write(t, "proxy-1.arm64.json", `{"matches":[`+inBinary("H4", "2026-11-01", "/usr/bin/server")+`]}`)
	if code, out := gate(t, "-policy", policy, "-record", recorded, fresh, twin); code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	got, err := os.ReadFile(recorded)
	if err != nil {
		t.Fatal(err)
	}
	want := `[
  {
    "component": "server",
    "package": "pkg-H3",
    "id": "H3"
  },
  {
    "component": "server",
    "package": "pkg-H4",
    "id": "H4"
  }
]
`
	if string(got) != want {
		t.Errorf("recorded:\n%s", got)
	}
	later := write(t, "proxy-2.json", `{"matches":[`+inBinary("H3", "2026-09-01", "/usr/bin/server")+`]}`)
	if code, out := gate(t, "-policy", policy, "-baseline", recorded, later); code != 0 {
		t.Errorf("with the record as baseline: exit %d\n%s", code, out)
	}
	failing := filepath.Join(t.TempDir(), "failing.json")
	if code, _ := gate(t, "-policy", policy, "-record", failing, later); code != 1 {
		t.Errorf("without baseline: exit %d", code)
	}
	if _, err := os.Stat(failing); !os.IsNotExist(err) {
		t.Errorf("a failing gate recorded: %v", err)
	}
}

// -report tracks every third-party finding and fails only on ours past
// its deadline; its record is the deployed release's baseline.
func TestReportTracksWhatIsDeployed(t *testing.T) {
	policy := write(t, "policy.json", proxyPolicy)
	theirs := write(t, "proxy-1.json", `{"matches":[`+inBinary("H3", "2026-09-01", "/usr/bin/server")+`]}`)
	ours := write(t, "app.json", `{"matches":[`+match("C1", "Critical", "2026-10-01", "")+`]}`)
	recorded := filepath.Join(t.TempDir(), "baseline.json")
	if code, out := gate(t, "-report", "-policy", policy, "-record", recorded, theirs); code != 0 || !strings.Contains(out, "server  tracked") || !strings.Contains(out, "1 tracked in third-party components") {
		t.Errorf("third-party only: exit %d\n%s", code, out)
	}
	if b, err := os.ReadFile(recorded); err != nil || !strings.Contains(string(b), `"id": "H3"`) {
		t.Errorf("record: %v\n%s", err, b)
	}
	if code, out := gate(t, "-report", "-policy", policy, theirs, ours); code != 1 || !strings.Contains(out, "own  LATE: due 2026-10-08") {
		t.Errorf("ours late: exit %d\n%s", code, out)
	}
}

// A component that names nothing to match, or a named baseline or policy
// that is missing, is bad input.
func TestBadPolicyOrBaselineIsNoPass(t *testing.T) {
	report := write(t, "image.json", `{"matches":[]}`)
	for name, args := range map[string][]string{
		"component without match": {"-policy", write(t, "p.json", `{"thirdParty":[{"component":"x"}]}`)},
		"component without name":  {"-policy", write(t, "p.json", `{"thirdParty":[{"image":"x"}]}`)},
		"bad pattern":             {"-policy", write(t, "p.json", `{"thirdParty":[{"component":"x","path":"["}]}`)},
		"missing policy":          {"-policy", filepath.Join(t.TempDir(), "missing.json")},
		"missing baseline":        {"-baseline", filepath.Join(t.TempDir(), "missing.json")},
		"incomplete baseline":     {"-baseline", write(t, "b.json", `[{"component":"x","id":"y"}]`)},
	} {
		if code, out := gate(t, append(args, report)...); code != 2 {
			t.Errorf("%s: exit %d\n%s", name, code, out)
		}
	}
}
