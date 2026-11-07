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

func base(entries string) string {
	return `{"release":"1.0","recorded":"2026-10-01","findings":[` + entries + `]}`
}

func inherited(component, pkg, id string) string {
	return `{"component":"` + component + `","package":"` + pkg + `","id":"` + id + `","state":"inherited"}`
}

// An inherited third-party finding is tracked, not failed, under its ID
// or an alias; one this release adds is held to its deadline from the
// package's fix; one in what we own is held to it whatever the baseline
// says.
func TestThirdPartyFindingsFailOnlyWhenNew(t *testing.T) {
	policy := write(t, "policy.json", proxyPolicy)
	old := "2026-09-01"
	baseline := write(t, "baseline.json", base(inherited("server", "pkg-H1", "H1")+","+inherited("server", "pkg-H2", "CVE-2026-9")+","+inherited("server", "pkg-O1", "O1")))
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

// An inherited known exploited finding fails once inheritedKev (14 days by
// default, set in the policy) has passed since the package's fix.
func TestAnInheritedKnownExploitedFindingEscalates(t *testing.T) {
	exploited := func(fixedOn string) string {
		return write(t, "proxy-1.json", `{"matches":[`+strings.Replace(inBinary("K1", fixedOn, "/usr/bin/server"), `"fix":`, `"knownExploited":[{"cve":"CVE-2026-1"}],"fix":`, 1)+`]}`)
	}
	baseline := write(t, "baseline.json", base(inherited("server", "pkg-K1", "K1")))
	policy := write(t, "policy.json", proxyPolicy)
	if code, out := gate(t, "-policy", policy, "-baseline", baseline, exploited("2026-10-25")); code != 0 || !strings.Contains(out, "known exploited: fails 2026-11-08") {
		t.Errorf("11 days: exit %d\n%s", code, out)
	}
	if code, out := gate(t, "-policy", policy, "-baseline", baseline, exploited("2026-10-20")); code != 1 || !strings.Contains(out, "LATE: inherited, known exploited, due 2026-11-03") {
		t.Errorf("16 days: exit %d\n%s", code, out)
	}
	longer := write(t, "policy.json", `{"deadlines":{"inheritedKev":"30d"},"thirdParty":[{"component":"server","path":"/usr/bin/server"}]}`)
	if code, out := gate(t, "-policy", longer, "-baseline", baseline, exploited("2026-10-20")); code != 0 {
		t.Errorf("inheritedKev 30d: exit %d\n%s", code, out)
	}
}

// An upstream image is a component by its repository, whichever digest.
func TestAnUpstreamImageIsAComponent(t *testing.T) {
	const digest = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
	report := write(t, "db.json", `{"matches":[`+match("C1", "Critical", "2026-10-01", "")+`],"source":{"type":"image","target":{"userInput":"registry:docker.io/example/db:2.0@`+digest+`"}}}`)
	policy := write(t, "policy.json", proxyPolicy)
	if code, out := gate(t, "-policy", policy, "-baseline", write(t, "b.json", base(inherited("db", "pkg-C1", "C1"))), report); code != 0 || !strings.Contains(out, "db  tracked") {
		t.Errorf("inherited: exit %d\n%s", code, out)
	}
	if code, out := gate(t, "-policy", policy, "-baseline", write(t, "b.json", base("")), report); code != 1 {
		t.Errorf("new: exit %d\n%s", code, out)
	}
}

func gateAt(t *testing.T, at time.Time, args ...string) (int, string) {
	t.Helper()
	var out bytes.Buffer
	code := run(args, &out, &out, at)
	return code, out.String()
}

// A new finding a gate records stays new: the next gates hold it to the
// due date recorded, so recording never turns it into an inherited one,
// known exploited or not. Only the initial baseline of what is deployed
// marks findings inherited.
func TestARecordedNewFindingKeepsItsDeadline(t *testing.T) {
	policy := write(t, "policy.json", proxyPolicy)
	for name, c := range map[string]struct {
		fixedOn, extra, due string
		later               time.Time
	}{
		"high":            {"2026-11-01", "", "2026-12-01", time.Date(2026, 12, 2, 0, 0, 0, 0, time.UTC)},
		"known exploited": {"2026-11-04", `"knownExploited":[{"cve":"CVE-2026-1"}],`, "2026-11-07", time.Date(2026, 11, 8, 0, 0, 0, 0, time.UTC)},
	} {
		finding := strings.Replace(inBinary("H1", c.fixedOn, "/usr/bin/server"), `"fix":`, c.extra+`"fix":`, 1)
		report := write(t, "proxy-1.json", `{"matches":[`+finding+`]}`)
		first := filepath.Join(t.TempDir(), "first.json")
		if code, out := gate(t, "-policy", policy, "-baseline", write(t, "empty.json", base("")), "-record", first, "-release", "1.0", report); code != 0 || !strings.Contains(out, "new, due "+c.due) {
			t.Fatalf("%s, release 1.0: exit %d\n%s", name, code, out)
		}
		second := filepath.Join(t.TempDir(), "second.json")
		if code, out := gate(t, "-policy", policy, "-baseline", first, "-record", second, "-release", "1.1", report); code != 0 {
			t.Fatalf("%s, release 1.1: exit %d\n%s", name, code, out)
		}
		got, _ := os.ReadFile(second)
		for _, want := range []string{`"release": "1.1"`, `"state": "new"`, `"firstSeen": "2026-11-05"`, `"due": "` + c.due + `"`} {
			if !strings.Contains(string(got), want) {
				t.Errorf("%s: second record has no %s:\n%s", name, want, got)
			}
		}
		if code, out := gateAt(t, c.later, "-policy", policy, "-baseline", second, report); code != 1 || !strings.Contains(out, "LATE: new, due "+c.due) {
			t.Errorf("%s, past its due date: exit %d\n%s", name, code, out)
		}
	}
}

// -record writes the third-party findings once each, sorted, with this
// release; it needs -release, and a failing gate writes nothing.
func TestARecordIsTheNextBaseline(t *testing.T) {
	policy := write(t, "policy.json", proxyPolicy)
	recorded := filepath.Join(t.TempDir(), "baseline.json")
	fresh := write(t, "proxy-1.amd64.json", `{"matches":[`+inBinary("H4", "2026-11-01", "/usr/bin/server")+`,`+inBinary("H3", "", "/usr/bin/server")+`,`+inBinary("O1", "2026-11-01", "/lib/apk/db/installed")+`]}`)
	twin := write(t, "proxy-1.arm64.json", `{"matches":[`+inBinary("H4", "2026-11-01", "/usr/bin/server")+`]}`)
	if code, out := gate(t, "-policy", policy, "-baseline", write(t, "empty.json", base("")), "-record", recorded, "-release", "2.0", fresh, twin); code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	got, err := os.ReadFile(recorded)
	if err != nil {
		t.Fatal(err)
	}
	want := `{
  "release": "2.0",
  "recorded": "2026-11-05",
  "findings": [
    {
      "component": "server",
      "package": "pkg-H3",
      "id": "H3",
      "state": "new",
      "firstSeen": "2026-11-05"
    },
    {
      "component": "server",
      "package": "pkg-H4",
      "id": "H4",
      "state": "new",
      "firstSeen": "2026-11-05",
      "due": "2026-12-01"
    }
  ]
}
`
	if string(got) != want {
		t.Errorf("recorded:\n%s", got)
	}
	if code, _ := gate(t, "-policy", policy, "-record", recorded, fresh); code != 2 {
		t.Errorf("-record without -release: exit %d", code)
	}
	late := write(t, "proxy-2.json", `{"matches":[`+inBinary("H5", "2026-09-01", "/usr/bin/server")+`]}`)
	failing := filepath.Join(t.TempDir(), "failing.json")
	if code, _ := gate(t, "-policy", policy, "-baseline", write(t, "empty.json", base("")), "-record", failing, "-release", "2.1", late); code != 1 {
		t.Errorf("late: exit %d", code)
	}
	if _, err := os.Stat(failing); !os.IsNotExist(err) {
		t.Errorf("a failing gate recorded: %v", err)
	}
}

// The baseline's release is printed; with -previous, a baseline of
// neither the previous release nor this one (a rerun) is refused.
func TestTheBaselineMustBeOfThePreviousRelease(t *testing.T) {
	report := write(t, "image.json", `{"matches":[]}`)
	baseline := write(t, "b.json", base(""))
	if code, out := gate(t, "-baseline", baseline, report); code != 0 || !strings.Contains(out, "baseline of release 1.0, recorded 2026-10-01") {
		t.Errorf("printed: exit %d\n%s", code, out)
	}
	if code, out := gate(t, "-baseline", baseline, "-previous", "1.0", report); code != 0 {
		t.Errorf("previous: exit %d\n%s", code, out)
	}
	if code, out := gate(t, "-baseline", baseline, "-previous", "0.9", "-record", filepath.Join(t.TempDir(), "r.json"), "-release", "1.0", report); code != 0 {
		t.Errorf("a rerun: exit %d\n%s", code, out)
	}
	if code, out := gate(t, "-baseline", baseline, "-previous", "1.1", report); code != 2 || !strings.Contains(out, "not of the previous release 1.1") {
		t.Errorf("an older baseline: exit %d\n%s", code, out)
	}
}

// -report holds findings by the same rules; only -report -record -initial
// marks every third-party finding inherited, writing the first baseline.
func TestReportTracksWhatIsDeployed(t *testing.T) {
	policy := write(t, "policy.json", proxyPolicy)
	theirs := write(t, "proxy-1.json", `{"matches":[`+inBinary("H3", "2026-09-01", "/usr/bin/server")+`]}`)
	ours := write(t, "app.json", `{"matches":[`+match("C1", "Critical", "2026-10-01", "")+`]}`)
	recorded := filepath.Join(t.TempDir(), "baseline.json")
	if code, out := gate(t, "-report", "-initial", "-policy", policy, "-record", recorded, "-release", "1.0", theirs); code != 0 || !strings.Contains(out, "server  tracked") || !strings.Contains(out, "1 tracked in third-party components") {
		t.Errorf("third-party only: exit %d\n%s", code, out)
	}
	if b, err := os.ReadFile(recorded); err != nil || !strings.Contains(string(b), `"state": "inherited"`) {
		t.Errorf("record: %v\n%s", err, b)
	}
	if code, out := gate(t, "-report", "-policy", policy, "-baseline", write(t, "b0.json", base(inherited("server", "pkg-H3", "H3"))), theirs, ours); code != 1 || !strings.Contains(out, "own  LATE: due 2026-10-08") || !strings.Contains(out, "server  tracked") {
		t.Errorf("ours late: exit %d\n%s", code, out)
	}
	if code, out := gate(t, "-report", "-policy", policy, "-baseline", write(t, "b.json", base("")), theirs); code != 1 || !strings.Contains(out, "LATE: new") {
		t.Errorf("with a baseline: exit %d\n%s", code, out)
	}
}

// A component that names nothing to match, or a named baseline or policy
// that is missing or incomplete, is bad input.
func TestBadPolicyOrBaselineIsNoPass(t *testing.T) {
	report := write(t, "image.json", `{"matches":[]}`)
	for name, args := range map[string][]string{
		"component without match":   {"-policy", write(t, "p.json", `{"thirdParty":[{"component":"x"}]}`)},
		"component without name":    {"-policy", write(t, "p.json", `{"thirdParty":[{"image":"x"}]}`)},
		"bad pattern":               {"-policy", write(t, "p.json", `{"thirdParty":[{"component":"x","path":"["}]}`)},
		"missing policy":            {"-policy", filepath.Join(t.TempDir(), "missing.json")},
		"missing baseline":          {"-baseline", filepath.Join(t.TempDir(), "missing.json")},
		"baseline without state":    {"-baseline", write(t, "b.json", base(`{"component":"x","package":"p","id":"y"}`))},
		"baseline without release":  {"-baseline", write(t, "b.json", `{"findings":[]}`)},
		"old baseline list":         {"-baseline", write(t, "b.json", `[]`)},
		"previous without baseline": {"-previous", "1.0"},
	} {
		if code, out := gate(t, append(args, report)...); code != 2 {
			t.Errorf("%s: exit %d\n%s", name, code, out)
		}
	}
}

// A baseline that is missing never makes findings inherited: -record
// needs -baseline unless -initial, -initial needs -report -record and no
// -baseline, and a report without a baseline holds every third-party
// finding as new.
func TestOnlyInitialMarksFindingsInherited(t *testing.T) {
	policy := write(t, "policy.json", proxyPolicy)
	theirs := write(t, "proxy-1.json", `{"matches":[`+inBinary("H3", "2026-09-01", "/usr/bin/server")+`]}`)
	record := func() string { return filepath.Join(t.TempDir(), "r.json") }
	for name, args := range map[string][]string{
		"gate record without baseline":   {"-record", record(), "-release", "1.0"},
		"report record without baseline": {"-report", "-record", record(), "-release", "1.0"},
		"initial without report":         {"-initial", "-record", record(), "-release", "1.0"},
		"initial without record":         {"-report", "-initial"},
		"initial with a baseline":        {"-report", "-initial", "-baseline", write(t, "b.json", base("")), "-record", record(), "-release", "1.0"},
	} {
		if code, out := gate(t, append(append([]string{"-policy", policy}, args...), theirs)...); code != 2 {
			t.Errorf("%s: exit %d\n%s", name, code, out)
		}
	}
	if code, out := gate(t, "-report", "-policy", policy, theirs); code != 1 || !strings.Contains(out, "LATE: new, due 2026-10-01") {
		t.Errorf("a report without baseline: exit %d\n%s", code, out)
	}
}
