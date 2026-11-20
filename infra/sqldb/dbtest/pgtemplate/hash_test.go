package pgtemplate

import (
	"runtime/debug"
	"testing"
	"testing/fstest"
)

var deps = []*debug.Module{{Path: "example.com/migrate", Version: "v1.2.0"}, {Path: "example.com/other", Version: "v0.1.0"}}

func sumOf(t *testing.T, files fstest.MapFS, deps []*debug.Module, extra ...string) string {
	t.Helper()
	got, err := sum([]Set{{Name: "public.schema_version", Files: files}}, []string{"example.com/migrate"}, deps, extra)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestTheSameStateSumsAlike(t *testing.T) {
	files := fstest.MapFS{"00001_init.sql": {Data: []byte("CREATE TABLE a (id int)")}}
	first := sumOf(t, files, deps, "grants")
	if second := sumOf(t, files, deps, "grants"); first != second {
		t.Fatal("one state summed two ways")
	}
}

func TestEveryPartOfTheStateChangesTheSum(t *testing.T) {
	files := fstest.MapFS{"00001_init.sql": {Data: []byte("CREATE TABLE a (id int)")}}
	base := sumOf(t, files, deps, "grants")
	changed := map[string]string{
		"a file's body": sumOf(t, fstest.MapFS{"00001_init.sql": {Data: []byte("CREATE TABLE b (id int)")}}, deps, "grants"),
		"a file's name": sumOf(t, fstest.MapFS{"00002_init.sql": {Data: []byte("CREATE TABLE a (id int)")}}, deps, "grants"),
		"a new file": sumOf(t, fstest.MapFS{
			"00001_init.sql": {Data: []byte("CREATE TABLE a (id int)")},
			"00002_more.sql": {Data: []byte("")},
		}, deps, "grants"),
		"a module's version": sumOf(t, files, []*debug.Module{{Path: "example.com/migrate", Version: "v1.3.0"}}, "grants"),
		"the extra":          sumOf(t, files, deps, "other grants"),
		"the extra's split":  sumOf(t, files, deps, "gra", "nts"),
	}
	for what, got := range changed {
		if got == base {
			t.Errorf("%s left the sum alike", what)
		}
	}
}

func TestAModuleNotNamedLeavesTheSumAlike(t *testing.T) {
	files := fstest.MapFS{"00001_init.sql": {Data: []byte("CREATE TABLE a (id int)")}}
	if sumOf(t, files, deps) != sumOf(t, files, deps[:1]) {
		t.Fatal("a module not named changed the sum")
	}
}

func TestAModuleTheBinaryLacksIsAnError(t *testing.T) {
	_, err := sum(nil, []string{"example.com/absent"}, deps, nil)
	if err == nil {
		t.Fatal("a module the binary lacks hashed")
	}
}

func TestHashReadsTheBinarysModules(t *testing.T) {
	if _, err := Hash(nil, []string{"github.com/jackc/pgx/v5"}); err != nil {
		t.Fatal(err)
	}
}

func TestTheSectionsOfTheStateStayApart(t *testing.T) {
	empty, err := sum([]Set{{Name: "a", Files: fstest.MapFS{}}}, nil, deps, []string{"x", "y"})
	if err != nil {
		t.Fatal(err)
	}
	file, err := sum([]Set{{Name: "a", Files: fstest.MapFS{"x": {Data: []byte("y")}}}}, nil, deps, nil)
	if err != nil {
		t.Fatal(err)
	}
	if empty == file {
		t.Fatal("extra text summed like a file")
	}
}

func TestAReplacedModuleSumsByItsReplacement(t *testing.T) {
	files := fstest.MapFS{"00001_init.sql": {Data: []byte("CREATE TABLE a (id int)")}}
	replaced := func(version string) []*debug.Module {
		return []*debug.Module{{Path: "example.com/migrate", Version: "v1.2.0", Replace: &debug.Module{Path: "example.com/fork", Version: version}}}
	}
	if sumOf(t, files, replaced("v1.0.0")) == sumOf(t, files, replaced("v1.0.1")) {
		t.Fatal("a new replacement left the sum alike")
	}
}
